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

// Cap eager allocation so a long input with few words does not reserve memory
// proportional to its byte length.
const maximumInitialTokenCapacity = 4096

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

// IDTokens contains known token IDs, omitted-word positions, and the byte range
// spanning the known tokens. UnknownAfter and StopwordAfter store the number of
// known tokens preceding each omitted word.
type IDTokens struct {
	IDs           []ID
	UnknownAfter  []uint32
	StopwordAfter []uint32
	Start         int
	End           int
}

// Word is a normalized word and its byte range in the original input.
type Word struct {
	Text  string
	Start int
	End   int
}

// Vocabulary is an immutable mapping from normalized words to integer IDs.
type Vocabulary struct {
	ids       map[string]ID
	words     []string
	stopwords []bool
}

// NewVocabulary builds a deterministic vocabulary from texts. IDs are assigned
// in normalized lexical order and start at one, leaving zero for Unknown.
func NewVocabulary(texts [][]byte) (*Vocabulary, error) {
	return NewVocabularyWithStopwords(texts, nil)
}

// NewVocabularyWithStopwords builds a vocabulary that omits stopwords from
// token sequences while retaining their IDs.
func NewVocabularyWithStopwords(texts [][]byte, stopwords []string) (*Vocabulary, error) {
	unique := make(map[string]struct{})
	var scratch []byte
	for _, text := range texts {
		scan(text, func(start, end int) {
			addNormalized(unique, text[start:end], &scratch)
		})
	}
	for _, word := range stopwords {
		normalized := Words([]byte(word))
		if len(normalized) != 1 || normalized[0].Text != word ||
			normalized[0].Start != 0 || normalized[0].End != len(word) {
			return nil, fmt.Errorf("tokenize: invalid normalized stopword %q", word)
		}
		unique[word] = struct{}{}
	}

	words := make([]string, 0, len(unique))
	for word := range unique {
		words = append(words, word)
	}
	sort.Strings(words)
	if uint64(len(words)) >= uint64(^ID(0)) {
		return nil, fmt.Errorf("tokenize: %d words exceed the ID space", len(words))
	}
	vocabulary := newVocabulary(words)
	for _, word := range stopwords {
		id, exists := vocabulary.ids[word]
		if !exists || id == Unknown {
			return nil, fmt.Errorf("tokenize: invalid stopword %q", word)
		}
		vocabulary.stopwords[id] = true
	}
	return vocabulary, nil
}

// NewVocabularyFromWords loads an already normalized and sorted vocabulary.
func NewVocabularyFromWords(words []string) (*Vocabulary, error) {
	return NewVocabularyFromWordsWithStopwords(words, nil)
}

// NewVocabularyFromWordsWithStopwords loads normalized words and marks the
// one-based IDs in stopwords for omission from token sequences.
func NewVocabularyFromWordsWithStopwords(
	words []string,
	stopwords []uint32,
) (*Vocabulary, error) {
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
	vocabulary := newVocabulary(slices.Clone(words))
	var previous uint32
	for index, rawID := range stopwords {
		if rawID == 0 || uint64(rawID) > uint64(len(words)) {
			return nil, fmt.Errorf("tokenize: invalid stopword ID %d", rawID)
		}
		if index > 0 && rawID <= previous {
			return nil, fmt.Errorf("tokenize: stopword IDs are not strictly sorted at %d", rawID)
		}
		vocabulary.stopwords[ID(rawID)] = true
		previous = rawID
	}
	return vocabulary, nil
}

func newVocabulary(words []string) *Vocabulary {
	byID := make([]string, len(words)+1)
	ids := make(map[string]ID, len(words))
	for index, word := range words {
		id := ID(index + 1)
		ids[word] = id
		byID[id] = word
	}
	return &Vocabulary{
		ids:       ids,
		words:     byID,
		stopwords: make([]bool, len(byID)),
	}
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
		if v.stopwords[id] {
			return
		}
		ids = append(ids, id)
		offsets = append(offsets, Offset{Start: start, End: end})
	})
	return Tokens{IDs: ids, Offsets: offsets}
}

// TokenizeIDs normalizes input and maps known words to IDs. Start and End span
// the first through last known token without retaining every token offset.
func (v *Vocabulary) TokenizeIDs(input []byte) IDTokens {
	capacity := min(len(input)/averageWordBytes, maximumInitialTokenCapacity)
	return v.TokenizeIDsAppend(input, make([]ID, 0, capacity), nil, nil, nil)
}

// TokenizeIDsAppend is TokenizeIDs writing into ids[:0] and
// the position buffers. wordScratch, when provided, is reused for case
// normalization and may be grown.
func (v *Vocabulary) TokenizeIDsAppend(
	input []byte,
	ids []ID,
	unknownAfter []uint32,
	stopwordAfter []uint32,
	wordScratch *[]byte,
) IDTokens {
	result := IDTokens{
		IDs:           ids[:0],
		UnknownAfter:  unknownAfter[:0],
		StopwordAfter: stopwordAfter[:0],
	}
	var local []byte
	if wordScratch == nil {
		wordScratch = &local
	}
	scan(input, func(start, end int) {
		id := v.lookup(input[start:end], wordScratch)
		if id == Unknown {
			result.UnknownAfter = append(result.UnknownAfter, uint32(len(result.IDs)))
			return
		}
		if v.stopwords[id] {
			result.StopwordAfter = append(result.StopwordAfter, uint32(len(result.IDs)))
			return
		}
		if len(result.IDs) == 0 {
			result.Start = start
		}
		result.IDs = append(result.IDs, id)
		result.End = end
	})
	return result
}

// TokenOffsets returns every normalized word's byte range. tokenCount is a
// capacity hint and does not limit the number of returned offsets.
func TokenOffsets(input []byte, tokenCount int) []Offset {
	return TokenOffsetsAppend(input, make([]Offset, 0, max(tokenCount, 0)))
}

// TokenOffsetsAppend is TokenOffsets writing into offsets[:0].
func TokenOffsetsAppend(input []byte, offsets []Offset) []Offset {
	offsets = offsets[:0]
	scan(input, func(start, end int) {
		offsets = append(offsets, Offset{Start: start, End: end})
	})
	return offsets
}

// KnownTokenOffsetsAppend returns byte ranges for known words using the
// omitted-word positions returned by TokenizeIDsAppend.
func KnownTokenOffsetsAppend(
	input []byte,
	unknownAfter []uint32,
	stopwordAfter []uint32,
	offsets []Offset,
) []Offset {
	offsets = offsets[:0]
	unknownIndex := 0
	stopwordIndex := 0
	scan(input, func(start, end int) {
		if unknownIndex < len(unknownAfter) &&
			unknownAfter[unknownIndex] == uint32(len(offsets)) {
			unknownIndex++
			return
		}
		if stopwordIndex < len(stopwordAfter) &&
			stopwordAfter[stopwordIndex] == uint32(len(offsets)) {
			stopwordIndex++
			return
		}
		offsets = append(offsets, Offset{Start: start, End: end})
	})
	return offsets
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
		isWord, width := asciiWord(input[offset]), 1
		if input[offset] >= utf8.RuneSelf {
			isWord, width = unicodeWordAt(input, offset)
		}
		if !isWord {
			offset += width
			continue
		}

		start := offset
		offset += width
		plusSeen := false
		for offset < len(input) {
			isWord, width = asciiWord(input[offset]), 1
			if input[offset] >= utf8.RuneSelf {
				isWord, width = unicodeWordAt(input, offset)
			}
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

func asciiWord(character byte) bool {
	return character >= 'a' && character <= 'z' ||
		character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9'
}

func unicodeWordAt(input []byte, offset int) (bool, int) {
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
