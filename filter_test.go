package licenses

import (
	"testing"

	"github.com/git-pkgs/licenses/internal/corpus"
)

func TestFilterContainedMatchesKeepsLargestSpan(t *testing.T) {
	t.Parallel()

	matches := []exactMatch{
		{ruleIndex: 1, method: Exact, tokenStart: 2, tokenEnd: 4},
		{ruleIndex: 0, method: Exact, tokenStart: 0, tokenEnd: 6},
		{ruleIndex: 2, method: Exact, tokenStart: 8, tokenEnd: 10},
	}
	kept, discarded := filterContainedMatches(matches)
	if len(kept) != 2 ||
		kept[0].ruleIndex != 0 ||
		kept[1].ruleIndex != 2 {
		t.Fatalf("kept = %#v", kept)
	}
	if len(discarded) != 1 || discarded[0].ruleIndex != 1 {
		t.Fatalf("discarded = %#v", discarded)
	}
}

func TestFilterOverlappingMatchesKeepsLargerSpan(t *testing.T) {
	t.Parallel()

	engine := &matchEngine{rules: []corpus.Rule{
		{Expression: "outer"},
		{Expression: "inner"},
	}}
	matches := []exactMatch{
		{ruleIndex: 0, method: Exact, tokenStart: 0, tokenEnd: 10},
		{ruleIndex: 1, method: Exact, tokenStart: 3, tokenEnd: 12},
	}
	kept, discarded := filterOverlappingMatches(engine, matches)
	if len(kept) != 1 || kept[0].ruleIndex != 0 {
		t.Fatalf("kept = %#v", kept)
	}
	if len(discarded) != 1 || discarded[0].ruleIndex != 1 {
		t.Fatalf("discarded = %#v", discarded)
	}
}
