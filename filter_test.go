package licenses

import (
	"context"
	"errors"
	"testing"

	"github.com/git-pkgs/licenses/internal/corpus"
)

func TestFilterContainedMatchesKeepsLargestSpan(t *testing.T) {
	t.Parallel()

	matches := []exactMatch{
		{ruleIndex: 0, method: Exact, tokenStart: 0, tokenEnd: 6},
		{ruleIndex: 1, method: Exact, tokenStart: 2, tokenEnd: 4},
		{ruleIndex: 2, method: Exact, tokenStart: 8, tokenEnd: 10},
	}
	kept, discarded, err := filterContainedMatches(context.Background(), matches)
	if err != nil {
		t.Fatal(err)
	}
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
	kept, discarded, err := filterOverlappingMatches(context.Background(), engine, matches)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].ruleIndex != 0 {
		t.Fatalf("kept = %#v", kept)
	}
	if len(discarded) != 1 || discarded[0].ruleIndex != 1 {
		t.Fatalf("discarded = %#v", discarded)
	}
}

func TestFilterOverlappingMatchesRemovesCurrent(t *testing.T) {
	t.Parallel()

	engine := &matchEngine{rules: []corpus.Rule{
		{Expression: "short"},
		{Expression: "long"},
	}}
	matches := []exactMatch{
		{ruleIndex: 0, method: Exact, tokenStart: 0, tokenEnd: 9},
		{ruleIndex: 1, method: Exact, tokenStart: 2, tokenEnd: 12},
	}
	kept, discarded, err := filterOverlappingMatches(context.Background(), engine, matches)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].ruleIndex != 1 {
		t.Fatalf("kept = %#v", kept)
	}
	if len(discarded) != 1 || discarded[0].ruleIndex != 0 {
		t.Fatalf("discarded = %#v", discarded)
	}
}

func TestOverlappingMatchToRemove(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		currentLength int
		nextLength    int
		overlap       int
		sameLicensing bool
		want          overlapRemoval
	}{
		{name: "next extra large", currentLength: 10, nextLength: 5, overlap: 5, want: removeNext},
		{name: "current extra large", currentLength: 5, nextLength: 10, overlap: 5, want: removeCurrent},
		{name: "next large", currentLength: 10, nextLength: 8, overlap: 6, want: removeNext},
		{name: "current large", currentLength: 8, nextLength: 10, overlap: 6, want: removeCurrent},
		{
			name:          "same license next medium",
			currentLength: 10,
			nextLength:    10,
			overlap:       5,
			sameLicensing: true,
			want:          removeNext,
		},
		{
			name:          "same license current medium",
			currentLength: 8,
			nextLength:    10,
			overlap:       4,
			sameLicensing: true,
			want:          removeCurrent,
		},
		{name: "different licenses medium", currentLength: 10, nextLength: 10, overlap: 5},
		{
			name:          "same license small",
			currentLength: 10,
			nextLength:    10,
			overlap:       3,
			sameLicensing: true,
		},
	}
	for _, test := range tests {
		currentRule := corpus.Rule{Expression: "current"}
		nextRule := corpus.Rule{Expression: "next"}
		if test.sameLicensing {
			nextRule.Expression = currentRule.Expression
		}
		current := exactMatch{tokenEnd: test.currentLength}
		next := exactMatch{tokenEnd: test.nextLength}
		got := overlappingMatchToRemove(
			currentRule,
			nextRule,
			current,
			next,
			test.overlap,
		)
		if got != test.want {
			t.Errorf("%s: removal = %d, want %d", test.name, got, test.want)
		}
	}
}

func TestOverlapRemovalForPairUsesCombinedOverlap(t *testing.T) {
	t.Parallel()

	engine := &matchEngine{rules: []corpus.Rule{
		{Expression: "previous"},
		{Expression: "current"},
		{Expression: "next"},
	}}
	matches := []exactMatch{
		{ruleIndex: 0, tokenStart: 0, tokenEnd: 10},
		{ruleIndex: 1, tokenStart: 5, tokenEnd: 15},
		{ruleIndex: 2, tokenStart: 10, tokenEnd: 20},
	}
	if got := overlapRemovalForPair(engine, matches, 1, 2, 0); got != removeCurrent {
		t.Fatalf("removal = %d, want %d", got, removeCurrent)
	}
}

func TestOverlapRemovalForPairKeepsFalsePositivePair(t *testing.T) {
	t.Parallel()

	engine := &matchEngine{rules: []corpus.Rule{
		{Flags: corpus.FlagFalsePositive},
		{Flags: corpus.FlagFalsePositive},
	}}
	matches := []exactMatch{
		{ruleIndex: 0, tokenStart: 0, tokenEnd: 10},
		{ruleIndex: 1, tokenStart: 1, tokenEnd: 9},
	}
	if got := overlapRemovalForPair(engine, matches, 0, 1, -1); got != removeNeither {
		t.Fatalf("removal = %d, want %d", got, removeNeither)
	}
}

func TestRestoreNonOverlappingMergesInOrder(t *testing.T) {
	t.Parallel()

	kept := []exactMatch{
		{ruleIndex: 0, method: Exact, tokenStart: 0, tokenEnd: 2},
		{ruleIndex: 1, method: Exact, tokenStart: 8, tokenEnd: 10},
	}
	discarded := []exactMatch{
		{ruleIndex: 2, method: Exact, tokenStart: 1, tokenEnd: 3},
		{ruleIndex: 3, method: Exact, tokenStart: 4, tokenEnd: 7},
		{ruleIndex: 4, method: Exact, tokenStart: 5, tokenEnd: 6},
		{ruleIndex: 5, method: Exact, tokenStart: 9, tokenEnd: 11},
	}
	got, err := restoreNonOverlapping(context.Background(), kept, discarded)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 ||
		got[0].ruleIndex != 0 ||
		got[1].ruleIndex != 3 ||
		got[2].ruleIndex != 4 ||
		got[3].ruleIndex != 1 {
		t.Fatalf("restored matches = %#v", got)
	}
}

func TestFiltersReturnContextError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	matches := []exactMatch{
		{ruleIndex: 0, method: Exact, tokenStart: 0, tokenEnd: 4},
		{ruleIndex: 1, method: Exact, tokenStart: 1, tokenEnd: 2},
	}
	if _, _, err := filterContainedMatches(ctx, matches); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
