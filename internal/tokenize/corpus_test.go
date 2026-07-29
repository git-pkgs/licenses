package tokenize

import (
	"testing"

	"github.com/git-pkgs/licenses/internal/corpus"
)

func TestEmbeddedCorpusTokenizesWithoutUnknownWords(t *testing.T) {
	index, err := corpus.Load()
	if err != nil {
		t.Fatal(err)
	}
	vocabulary, err := NewVocabularyFromWords(index.Vocabulary)
	if err != nil {
		t.Fatal(err)
	}

	var tokenCount int
	for _, rule := range index.Rules {
		for position, id := range rule.Tokens {
			if id == uint32(Unknown) || int(id) > vocabulary.Len() {
				t.Fatalf("%s has invalid token %d at position %d", rule.ID, id, position)
			}
		}
		tokenCount += len(rule.Tokens)
	}
	const expectedTokenCount = 6_450_806
	if tokenCount != expectedTokenCount {
		t.Fatalf("token count = %d, want %d", tokenCount, expectedTokenCount)
	}
	const expectedVocabularySize = 30_069
	if vocabulary.Len() != expectedVocabularySize {
		t.Fatalf("vocabulary size = %d, want %d", vocabulary.Len(), expectedVocabularySize)
	}
}
