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

	for _, rule := range index.Rules {
		for position, id := range rule.Tokens {
			if id == uint32(Unknown) || int(id) > vocabulary.Len() {
				t.Fatalf("%s has invalid token %d at position %d", rule.ID, id, position)
			}
		}
	}
}
