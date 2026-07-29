// Package tokenize converts license text into normalized integer tokens.
package tokenize

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ID identifies a normalized word in a Vocabulary.
type ID uint32

// Unknown is the ID assigned to words absent from a Vocabulary.
const Unknown ID = 0

const averageWordBytes = 6

// Offset is a half-open byte range in the original input.
type Offset struct {
	Start int
	End   int
}

// Tokens contains token IDs and their corresponding input byte ranges.
type Tokens struct {
	IDs     []ID
	Offsets []Offset
}

// Word is a normalized word and its byte range in the original input.
type Word struct {
	Text  string
	Start int
	End   int
}

// Vocabulary is an immutable mapping from normalized words to integer IDs.
type Vocabulary struct {
	ids   map[string]ID
	words []string
}

// NewVocabulary builds a deterministic vocabulary from texts. IDs are assigned
// in normalized lexical order and start at one, leaving zero for Unknown.
func NewVocabulary(texts [][]byte) (*Vocabulary, error) {
	unique := make(map[string]struct{})
	var scratch []byte
	for _, text := range texts {
		scan(text, func(start, end int) {
			addNormalized(unique, text[start:end], &scratch)
		})
	}

	words := make([]string, 0, len(unique))
	for word := range unique {
		words = append(words, word)
	}
	sort.Strings(words)
	if uint64(len(words)) >= uint64(^ID(0)) {
		return nil, fmt.Errorf("tokenize: %d words exceed the ID space", len(words))
	}
	return newVocabulary(words), nil
}

// NewVocabularyFromWords loads an already normalized and sorted vocabulary.
func NewVocabularyFromWords(words []string) (*Vocabulary, error) {
	for index, word := range words {
		if word == "" {
			return nil, fmt.Errorf("tokenize: vocabulary word %d is empty", index)
		}
		if index > 0 && words[index-1] >= word {
			return nil, fmt.Errorf("tokenize: vocabulary is not strictly sorted at %q", word)
		}
	}
	if uint64(len(words)) >= uint64(^ID(0)) {
		return nil, fmt.Errorf("tokenize: %d words exceed the ID space", len(words))
	}
	return newVocabulary(slices.Clone(words)), nil
}

func newVocabulary(words []string) *Vocabulary {
	byID := make([]string, len(words)+1)
	ids := make(map[string]ID, len(words))
	for index, word := range words {
		id := ID(index + 1)
		ids[word] = id
		byID[id] = word
	}
	return &Vocabulary{ids: ids, words: byID}
}

// Len returns the number of known words, excluding Unknown.
func (v *Vocabulary) Len() int {
	return len(v.words) - 1
}

// Lookup returns the ID for a normalized word.
func (v *Vocabulary) Lookup(word string) (ID, bool) {
	id, ok := v.ids[word]
	return id, ok
}

// Word returns the normalized word for id, or an empty string for an unknown ID.
func (v *Vocabulary) Word(id ID) string {
	if uint64(id) >= uint64(len(v.words)) {
		return ""
	}
	return v.words[id]
}

// Words returns the normalized words in ID order, excluding Unknown.
func (v *Vocabulary) Words() []string {
	return slices.Clone(v.words[1:])
}

// Tokenize normalizes input and maps every word to an ID and byte range.
func (v *Vocabulary) Tokenize(input []byte) Tokens {
	capacity := len(input) / averageWordBytes
	ids := make([]ID, 0, capacity)
	offsets := make([]Offset, 0, capacity)
	var scratch []byte
	scan(input, func(start, end int) {
		id := v.lookup(input[start:end], &scratch)
		ids = append(ids, id)
		offsets = append(offsets, Offset{Start: start, End: end})
	})
	return Tokens{IDs: ids, Offsets: offsets}
}

// Words returns normalized words and their byte ranges without mapping IDs.
func Words(input []byte) []Word {
	words := make([]Word, 0, len(input)/averageWordBytes)
	scan(input, func(start, end int) {
		words = append(words, Word{
			Text:  normalize(input[start:end]),
			Start: start,
			End:   end,
		})
	})
	return words
}

func scan(input []byte, yield func(start, end int)) {
	for offset := 0; offset < len(input); {
		isWord, width := wordAt(input, offset)
		if !isWord {
			offset += width
			continue
		}

		start := offset
		offset += width
		plusSeen := false
		for offset < len(input) {
			isWord, width = wordAt(input, offset)
			if isWord {
				offset += width
				continue
			}
			if input[offset] == '+' && !plusSeen {
				plusSeen = true
				offset++
				continue
			}
			break
		}
		yield(start, offset)
	}
}

func wordAt(input []byte, offset int) (bool, int) {
	if input[offset] < utf8.RuneSelf {
		character := input[offset]
		isWord := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9'
		return isWord, 1
	}
	character, width := utf8.DecodeRune(input[offset:])
	return unicode.IsLetter(character) || unicode.IsNumber(character), width
}

func (v *Vocabulary) lookup(word []byte, scratch *[]byte) ID {
	ascii, upper := caseProperties(word)
	if ascii && !upper {
		return v.ids[string(word)]
	}
	if ascii {
		lower := lowerASCII(word, scratch)
		return v.ids[string(lower)]
	}
	return v.ids[normalizeCase(word, ascii)]
}

func addNormalized(words map[string]struct{}, word []byte, scratch *[]byte) {
	ascii, upper := caseProperties(word)
	if ascii && !upper {
		if _, exists := words[string(word)]; !exists {
			words[string(word)] = struct{}{}
		}
		return
	}
	if ascii {
		lower := lowerASCII(word, scratch)
		if _, exists := words[string(lower)]; !exists {
			words[string(lower)] = struct{}{}
		}
		return
	}
	words[normalizeCase(word, ascii)] = struct{}{}
}

func lowerASCII(word []byte, scratch *[]byte) []byte {
	if cap(*scratch) < len(word) {
		*scratch = make([]byte, len(word))
	}
	lower := (*scratch)[:len(word)]
	for index, character := range word {
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		lower[index] = character
	}
	return lower
}

func normalize(word []byte) string {
	ascii, upper := caseProperties(word)
	if ascii && !upper {
		return string(word)
	}
	return normalizeCase(word, ascii)
}

func caseProperties(word []byte) (ascii, upper bool) {
	ascii = true
	for _, character := range word {
		if character >= utf8.RuneSelf {
			ascii = false
			break
		}
		if character >= 'A' && character <= 'Z' {
			upper = true
		}
	}
	return ascii, upper
}

func normalizeCase(word []byte, ascii bool) string {
	if ascii {
		lower := make([]byte, len(word))
		for index, character := range word {
			if character >= 'A' && character <= 'Z' {
				character += 'a' - 'A'
			}
			lower[index] = character
		}
		return string(lower)
	}
	return strings.ToLower(string(word))
}
