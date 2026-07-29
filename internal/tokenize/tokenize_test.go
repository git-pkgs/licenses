package tokenize

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

type scanCodeFixture struct {
	SourceCommit string         `json:"source_commit"`
	Cases        []scanCodeCase `json:"cases"`
}

type scanCodeCase struct {
	Name   string   `json:"name"`
	Input  string   `json:"input"`
	Tokens []string `json:"tokens"`
}

func TestWordsConformToScanCodeQueryTokenizer(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/scancode_query_tokenizer.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture scanCodeFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) < 40 {
		t.Fatalf("fixture has %d cases, want at least 40", len(fixture.Cases))
	}
	version, err := os.ReadFile("../../CORPUS_VERSION")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(version, []byte("commit="+fixture.SourceCommit)) {
		t.Fatalf("fixture commit %s differs from CORPUS_VERSION", fixture.SourceCommit)
	}

	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			words := Words([]byte(test.Input))
			got := make([]string, len(words))
			for index, word := range words {
				got[index] = word.Text
			}
			if !slices.Equal(got, test.Tokens) {
				t.Fatalf("tokens differ:\n got: %#v\nwant: %#v", got, test.Tokens)
			}
		})
	}
}

func TestWords(t *testing.T) {
	t.Parallel()

	input := []byte("  MIT-License\tGPL2+ and CAFÉ.\nAGPL-3.0")
	got := Words(input)
	wantText := []string{"mit", "license", "gpl2+", "and", "café", "agpl", "3", "0"}
	if len(got) != len(wantText) {
		t.Fatalf("got %d words, want %d: %#v", len(got), len(wantText), got)
	}
	for index, want := range wantText {
		if got[index].Text != want {
			t.Errorf("word %d = %q, want %q", index, got[index].Text, want)
		}
		raw := input[got[index].Start:got[index].End]
		if !bytes.EqualFold(raw, []byte(want)) {
			t.Errorf("offset %d selects %q for %q", index, raw, want)
		}
	}
}

func TestWordsMatchScanCodePlusBehavior(t *testing.T) {
	t.Parallel()

	input := []byte("{{Hi}}some {{}}Text with{{noth+-_!@ing}} GPL+2 gpl++3 +alone")
	got := Words(input)
	want := []Word{
		{Text: "hi", Start: 2, End: 4},
		{Text: "some", Start: 6, End: 10},
		{Text: "text", Start: 15, End: 19},
		{Text: "with", Start: 20, End: 24},
		{Text: "noth+", Start: 26, End: 31},
		{Text: "ing", Start: 35, End: 38},
		{Text: "gpl+2", Start: 41, End: 46},
		{Text: "gpl+", Start: 47, End: 51},
		{Text: "3", Start: 52, End: 53},
		{Text: "alone", Start: 55, End: 60},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("words differ:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestVocabularyIsDeterministic(t *testing.T) {
	t.Parallel()

	first, err := NewVocabulary([][]byte{
		[]byte("Zlib MIT apache-2.0"),
		[]byte("GPL2+ mit"),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewVocabulary([][]byte{
		[]byte("mit GPL2+"),
		[]byte("apache-2.0 MIT ZLIB"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Len() != second.Len() {
		t.Fatalf("lengths differ: %d, %d", first.Len(), second.Len())
	}
	for id := ID(1); id <= ID(first.Len()); id++ {
		if first.Word(id) != second.Word(id) {
			t.Fatalf("ID %d differs: %q, %q", id, first.Word(id), second.Word(id))
		}
	}
}

func TestVocabularyLoadsLexicalWords(t *testing.T) {
	t.Parallel()

	words := []string{"apache", "mit", "zlib"}
	vocabulary, err := NewVocabularyFromWords(words)
	if err != nil {
		t.Fatal(err)
	}
	words[0] = "changed"
	if vocabulary.Word(1) != "apache" {
		t.Fatalf("word 1 = %q", vocabulary.Word(1))
	}
	if _, err := NewVocabularyFromWords([]string{"mit", "apache"}); err == nil {
		t.Fatal("accepted unsorted vocabulary")
	}
}

func TestTokenizeMapsOffsetsAndUnknownWords(t *testing.T) {
	t.Parallel()

	vocabulary, err := NewVocabulary([][]byte{[]byte("MIT Apache")})
	if err != nil {
		t.Fatal(err)
	}
	got := vocabulary.Tokenize([]byte("MIT, mystery; Apache"))
	mit, _ := vocabulary.Lookup("mit")
	apache, _ := vocabulary.Lookup("apache")
	wantIDs := []ID{mit, Unknown, apache}
	wantOffsets := []Offset{{0, 3}, {5, 12}, {14, 20}}
	if !slices.Equal(got.IDs, wantIDs) {
		t.Fatalf("IDs = %#v, want %#v", got.IDs, wantIDs)
	}
	if !slices.Equal(got.Offsets, wantOffsets) {
		t.Fatalf("offsets = %#v, want %#v", got.Offsets, wantOffsets)
	}
}

func TestVocabularyIsSafeForConcurrentTokenize(t *testing.T) {
	t.Parallel()

	input := []byte("Permission is hereby granted under MIT.")
	vocabulary, err := NewVocabulary([][]byte{input})
	if err != nil {
		t.Fatal(err)
	}
	want := vocabulary.Tokenize(input)
	const goroutines = 16
	const iterations = 100
	errors := make(chan error, goroutines)
	var group sync.WaitGroup
	for range goroutines {
		group.Add(1)
		go func() {
			defer group.Done()
			for range iterations {
				got := vocabulary.Tokenize(input)
				if !slices.Equal(got.IDs, want.IDs) || !slices.Equal(got.Offsets, want.Offsets) {
					errors <- fmt.Errorf("tokenization differs: %#v", got)
					return
				}
			}
		}()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestWordsTreatMalformedUTF8AsPunctuation(t *testing.T) {
	t.Parallel()

	input := []byte{'M', 'I', 'T', 0xff, 'G', 'P', 'L'}
	got := Words(input)
	want := []Word{
		{Text: "mit", Start: 0, End: 3},
		{Text: "gpl", Start: 4, End: 7},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("words = %#v, want %#v", got, want)
	}
}

func FuzzTokenize(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("MIT-License GPL2+"))
	f.Add([]byte("Café ΔΙΚΑΙΩΜΑ"))
	f.Add([]byte("{{noth+-_!@ing}}"))
	f.Add([]byte{0xff, 'M', 'I', 'T', 0xc0})

	f.Fuzz(func(t *testing.T, input []byte) {
		words := Words(input)
		vocabulary, err := NewVocabulary([][]byte{input})
		if err != nil {
			t.Fatal(err)
		}
		tokens := vocabulary.Tokenize(input)
		if len(tokens.IDs) != len(words) || len(tokens.Offsets) != len(words) {
			t.Fatalf(
				"lengths differ: IDs=%d offsets=%d words=%d",
				len(tokens.IDs),
				len(tokens.Offsets),
				len(words),
			)
		}
		checkFuzzTokens(t, input, words, vocabulary, tokens)

		again := vocabulary.Tokenize(input)
		if !slices.Equal(tokens.IDs, again.IDs) || !slices.Equal(tokens.Offsets, again.Offsets) {
			t.Fatal("tokenization is not deterministic")
		}
	})
}

func checkFuzzTokens(
	t *testing.T,
	input []byte,
	words []Word,
	vocabulary *Vocabulary,
	tokens Tokens,
) {
	t.Helper()
	previousEnd := 0
	for index, word := range words {
		if word.Text == "" || !utf8.ValidString(word.Text) {
			t.Fatalf("invalid normalized word %q", word.Text)
		}
		if word.Text != strings.ToLower(word.Text) {
			t.Fatalf("word is not lowercase: %q", word.Text)
		}
		if word.Start < previousEnd || word.Start >= word.End || word.End > len(input) {
			t.Fatalf("invalid offsets at %d: %#v for %d bytes", index, word, len(input))
		}
		offset := tokens.Offsets[index]
		if offset.Start != word.Start || offset.End != word.End {
			t.Fatalf("offset %d differs: %#v, %#v", index, offset, word)
		}
		if tokens.IDs[index] == Unknown {
			t.Fatalf("word %q was unknown in its own vocabulary", word.Text)
		}
		if vocabulary.Word(tokens.IDs[index]) != word.Text {
			t.Fatalf("ID %d maps to %q, want %q", tokens.IDs[index], vocabulary.Word(tokens.IDs[index]), word.Text)
		}
		previousEnd = word.End
	}
}
