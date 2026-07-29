package corpus

import (
	"bytes"
	"slices"
	"testing"

	"github.com/git-pkgs/licenses/internal/aho"
)

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	automaton, err := aho.Build([]aho.Pattern{
		{Tokens: []uint32{1, 2}, Value: 0},
		{Tokens: []uint32{3, 4}, Value: 1},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{
		Info: Info{
			Version:      "test-version",
			RuleCount:    2,
			SourceCommit: "0123456789abcdef",
		},
		Vocabulary: []string{"apache", "license", "permission", "zlib"},
		Rules: []Rule{
			{
				ID:              "a.LICENSE",
				Expression:      "apache-2.0",
				Tokens:          []uint32{1, 2},
				Flags:           FlagLicenseText,
				Relevance:       100,
				MinimumCoverage: 100,
			},
			{
				ID:                  "z.RULE",
				Expression:          "zlib",
				Tokens:              []uint32{3, 4},
				Language:            "en",
				ReferencedFilenames: []string{"LICENSE", "COPYING"},
				RequiredPhrases:     []string{"permission is granted"},
				Flags:               FlagLicenseNotice | FlagContinuous,
				Relevance:           90,
				MinimumCoverage:     80,
			},
		},
		Automaton: automaton,
	}

	var first bytes.Buffer
	if err := Write(&first, index); err != nil {
		t.Fatal(err)
	}
	var second bytes.Buffer
	if err := Write(&second, index); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("encoding is not deterministic")
	}

	got, err := Read(bytes.NewReader(first.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got.Info.RuleCount != 2 {
		t.Fatalf("rule count = %d, want 2", got.Info.RuleCount)
	}
	if got.Rules[0].ID != "a.LICENSE" || got.Rules[1].ID != "z.RULE" {
		t.Fatalf("rules not sorted: %q, %q", got.Rules[0].ID, got.Rules[1].ID)
	}
	z := got.Rules[1]
	if z.Expression != "zlib" || z.Language != "en" || z.Relevance != 90 || z.MinimumCoverage != 80 {
		t.Fatalf("decoded rule differs: %#v", z)
	}
	if !slices.Equal(z.Tokens, []uint32{3, 4}) {
		t.Fatalf("tokens = %#v", z.Tokens)
	}
	if !slices.Equal(got.Vocabulary, index.Vocabulary) {
		t.Fatalf("vocabulary = %#v", got.Vocabulary)
	}
	if err := got.Automaton.Validate(2); err != nil {
		t.Fatal(err)
	}
}

func TestWriteRejectsDuplicateRuleIDs(t *testing.T) {
	t.Parallel()

	index := Index{
		Info: Info{Version: "test", SourceCommit: "commit"},
		Rules: []Rule{
			{ID: "same.RULE", Expression: "mit"},
			{ID: "same.RULE", Expression: "apache-2.0"},
		},
	}
	if err := Write(&bytes.Buffer{}, index); err == nil {
		t.Fatal("Write accepted duplicate rule IDs")
	}
}

func TestWriteRejectsWrongRuleCount(t *testing.T) {
	t.Parallel()

	index := Index{
		Info:  Info{Version: "test", SourceCommit: "commit", RuleCount: 2},
		Rules: []Rule{{ID: "one.RULE", Expression: "mit"}},
	}
	if err := Write(&bytes.Buffer{}, index); err == nil {
		t.Fatal("Write accepted an incorrect rule count")
	}
}

func TestReadRejectsInvalidData(t *testing.T) {
	t.Parallel()

	if _, err := Read(bytes.NewReader([]byte("not a corpus"))); err == nil {
		t.Fatal("Read accepted invalid data")
	}
}
