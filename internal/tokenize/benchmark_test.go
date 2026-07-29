package tokenize

import (
	"strings"
	"testing"

	"github.com/git-pkgs/licenses/internal/corpus"
)

var (
	benchmarkVocabulary *Vocabulary
	benchmarkTokens     Tokens
)

func BenchmarkCorpusVocabularyLoad(b *testing.B) {
	index, err := corpus.Load()
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		benchmarkVocabulary, err = NewVocabularyFromWords(index.Vocabulary)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTokenizeApacheLicense(b *testing.B) {
	index, err := corpus.Load()
	if err != nil {
		b.Fatal(err)
	}
	vocabulary, err := NewVocabularyFromWords(index.Vocabulary)
	if err != nil {
		b.Fatal(err)
	}
	var apacheRule corpus.Rule
	for _, rule := range index.Rules {
		if rule.ID == "apache-2.0.LICENSE" {
			apacheRule = rule
			break
		}
	}
	if len(apacheRule.Tokens) == 0 {
		b.Fatal("apache-2.0.LICENSE is absent")
	}
	words := make([]string, len(apacheRule.Tokens))
	for position, id := range apacheRule.Tokens {
		words[position] = vocabulary.Word(ID(id))
	}
	apache := []byte(strings.Join(words, " "))

	b.SetBytes(int64(len(apache)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkTokens = vocabulary.Tokenize(apache)
	}
}
