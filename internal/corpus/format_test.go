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
				ID:         "a.LICENSE",
				Expression: "apache-2.0",
				Tokens:     []uint32{1, 2},
				Flags:      FlagLicenseText,
				Relevance:  100,
			},
			{
				ID:         "z.RULE",
				Expression: "zlib",
				Tokens:     []uint32{3, 4},
				Flags:      FlagLicenseNotice | FlagContinuous,
				Relevance:  90,
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
	if z.Expression != "zlib" || z.Relevance != 90 {
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

func TestRuleFlagValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		got  uint16
		want uint16
	}{
		{name: "license text", got: FlagLicenseText, want: 2},
		{name: "license notice", got: FlagLicenseNotice, want: 4},
		{name: "license tag", got: FlagLicenseTag, want: 8},
		{name: "license reference", got: FlagLicenseReference, want: 16},
		{name: "license intro", got: FlagLicenseIntro, want: 32},
		{name: "license clue", got: FlagLicenseClue, want: 64},
		{name: "false positive", got: FlagFalsePositive, want: 128},
		{name: "required phrase", got: FlagRequiredPhrase, want: 256},
		{name: "continuous", got: FlagContinuous, want: 512},
		{name: "deprecated", got: FlagDeprecated, want: 1024},
	}
	for _, test := range tests {
		if test.got != test.want {
			t.Errorf("%s flag = %d, want %d", test.name, test.got, test.want)
		}
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
