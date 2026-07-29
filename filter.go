package licenses

import (
	"cmp"
	"context"
	"slices"

	"github.com/git-pkgs/licenses/internal/corpus"
)

const (
	overlapMedium     = 0.40
	overlapLarge      = 0.70
	overlapExtraLarge = 0.90
	minimumMatchCount = 2
	filterContextMask = 4095
)

type exactMatch struct {
	ruleIndex  uint32
	method     Method
	tokenStart int
	tokenEnd   int
}

func (m exactMatch) length() int {
	return m.tokenEnd - m.tokenStart
}

type exactFilterOptions struct {
	contained     bool
	overlapping   bool
	falsePositive bool
}

var allExactFilters = exactFilterOptions{
	contained:     true,
	overlapping:   true,
	falsePositive: true,
}

func filterExactMatches(
	ctx context.Context,
	engine *matchEngine,
	matches []exactMatch,
	options exactFilterOptions,
) ([]exactMatch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if len(matches) >= minimumMatchCount {
		slices.SortStableFunc(matches, compareExactMatches)
	}

	var err error
	var discardedContained, discardedOverlapping []exactMatch
	if options.contained {
		matches, discardedContained, err = filterContainedMatches(ctx, matches)
		if err != nil {
			return nil, err
		}
	}
	if options.overlapping {
		matches, discardedOverlapping, err = filterOverlappingMatches(ctx, engine, matches)
		if err != nil {
			return nil, err
		}
	}
	if len(discardedContained) != 0 {
		matches, err = restoreNonOverlapping(ctx, matches, discardedContained)
		if err != nil {
			return nil, err
		}
	}
	if len(discardedOverlapping) != 0 {
		matches, err = restoreNonOverlapping(ctx, matches, discardedOverlapping)
		if err != nil {
			return nil, err
		}
	}
	if options.contained {
		matches, _, err = filterContainedMatches(ctx, matches)
		if err != nil {
			return nil, err
		}
	}
	if options.falsePositive {
		matches, err = filterFalsePositiveMatches(ctx, engine, matches)
		if err != nil {
			return nil, err
		}
	}

	sortExactMatches(engine, matches)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return matches, nil
}

// matches must be ordered by compareExactMatches.
func filterContainedMatches(
	ctx context.Context,
	matches []exactMatch,
) ([]exactMatch, []exactMatch, error) {
	if len(matches) < minimumMatchCount {
		return matches, nil, nil
	}

	keep := make([]bool, len(matches))
	maxEnd := -1
	for index, match := range matches {
		if err := checkFilterContext(ctx, index); err != nil {
			return nil, nil, err
		}
		if match.tokenEnd <= maxEnd {
			continue
		}
		keep[index] = true
		maxEnd = match.tokenEnd
	}
	kept, discarded := splitExactMatches(matches, keep)
	return kept, discarded, nil
}

// matches must be ordered by compareExactMatches.
func filterOverlappingMatches(
	ctx context.Context,
	engine *matchEngine,
	matches []exactMatch,
) ([]exactMatch, []exactMatch, error) {
	if len(matches) < minimumMatchCount {
		return matches, nil, nil
	}

	keep := make([]bool, len(matches))
	for index := range keep {
		keep[index] = true
	}
	previousKept := -1
	operations := 0
	for current := 0; current < len(matches)-1; current++ {
		if !keep[current] {
			continue
		}
		if err := checkFilterContext(ctx, operations); err != nil {
			return nil, nil, err
		}
		operations++
		for next := current + 1; next < len(matches); next++ {
			if !keep[next] {
				continue
			}
			if err := checkFilterContext(ctx, operations); err != nil {
				return nil, nil, err
			}
			operations++
			if matches[next].tokenStart >= matches[current].tokenEnd {
				break
			}
			switch overlapRemovalForPair(engine, matches, current, next, previousKept) {
			case removeNext:
				keep[next] = false
				continue
			case removeCurrent:
				keep[current] = false
			}
			if !keep[current] {
				break
			}
		}
		if keep[current] {
			previousKept = current
		}
	}
	kept, discarded := splitExactMatches(matches, keep)
	return kept, discarded, nil
}

func overlapRemovalForPair(
	engine *matchEngine,
	matches []exactMatch,
	current, next, previous int,
) overlapRemoval {
	overlap := matchOverlap(matches[current], matches[next])
	if overlap == 0 {
		return removeNeither
	}
	currentRule := engine.rules[matches[current].ruleIndex]
	nextRule := engine.rules[matches[next].ruleIndex]
	if currentRule.Flags&corpus.FlagFalsePositive != 0 &&
		nextRule.Flags&corpus.FlagFalsePositive != 0 {
		return removeNeither
	}
	if remove := overlappingMatchToRemove(
		currentRule,
		nextRule,
		matches[current],
		matches[next],
		overlap,
	); remove != removeNeither {
		return remove
	}
	if previous < 0 || matchOverlap(matches[previous], matches[next]) != 0 {
		return removeNeither
	}
	combinedOverlap := matchOverlap(matches[current], matches[previous]) + overlap
	if float64(combinedOverlap) >= float64(matches[current].length())*overlapExtraLarge {
		return removeCurrent
	}
	return removeNeither
}

type overlapRemoval uint8

const (
	removeNeither overlapRemoval = iota
	removeCurrent
	removeNext
)

func overlappingMatchToRemove(
	currentRule, nextRule corpus.Rule,
	current, next exactMatch,
	overlap int,
) overlapRemoval {
	currentLength := current.length()
	nextLength := next.length()
	nextRatio := float64(overlap) / float64(nextLength)
	currentRatio := float64(overlap) / float64(currentLength)

	if nextRatio >= overlapExtraLarge && currentLength >= nextLength {
		return removeNext
	}
	if currentRatio >= overlapExtraLarge && currentLength <= nextLength {
		return removeCurrent
	}
	if nextRatio >= overlapLarge && currentLength >= nextLength {
		return removeNext
	}
	if currentRatio >= overlapLarge && currentLength <= nextLength {
		return removeCurrent
	}

	sameLicensing := currentRule.Expression == nextRule.Expression
	if nextRatio >= overlapMedium && sameLicensing {
		if currentLength >= nextLength {
			return removeNext
		}
		return removeCurrent
	}
	if currentRatio >= overlapMedium && sameLicensing {
		if currentLength >= nextLength {
			return removeNext
		}
		return removeCurrent
	}
	return removeNeither
}

// matches and discarded must be ordered by compareExactMatches.
func restoreNonOverlapping(
	ctx context.Context,
	matches, discarded []exactMatch,
) ([]exactMatch, error) {
	type interval struct {
		start int
		end   int
	}

	unions := make([]interval, 0, len(matches))
	for index, match := range matches {
		if err := checkFilterContext(ctx, index); err != nil {
			return nil, err
		}
		if len(unions) == 0 || match.tokenStart >= unions[len(unions)-1].end {
			unions = append(unions, interval{start: match.tokenStart, end: match.tokenEnd})
			continue
		}
		if match.tokenEnd > unions[len(unions)-1].end {
			unions[len(unions)-1].end = match.tokenEnd
		}
	}

	restored := make([]exactMatch, 0, len(discarded))
	unionIndex := 0
	for index, candidate := range discarded {
		if err := checkFilterContext(ctx, index); err != nil {
			return nil, err
		}
		for unionIndex < len(unions) && unions[unionIndex].end <= candidate.tokenStart {
			unionIndex++
		}
		if unionIndex < len(unions) && unions[unionIndex].start < candidate.tokenEnd {
			continue
		}
		restored = append(restored, candidate)
	}
	return mergeExactMatches(matches, restored), nil
}

func mergeExactMatches(first, second []exactMatch) []exactMatch {
	if len(second) == 0 {
		return first
	}
	merged := make([]exactMatch, 0, len(first)+len(second))
	firstIndex, secondIndex := 0, 0
	for firstIndex < len(first) && secondIndex < len(second) {
		if compareExactMatches(first[firstIndex], second[secondIndex]) <= 0 {
			merged = append(merged, first[firstIndex])
			firstIndex++
		} else {
			merged = append(merged, second[secondIndex])
			secondIndex++
		}
	}
	merged = append(merged, first[firstIndex:]...)
	merged = append(merged, second[secondIndex:]...)
	return merged
}

func splitExactMatches(
	matches []exactMatch,
	keep []bool,
) ([]exactMatch, []exactMatch) {
	kept := make([]exactMatch, 0, len(matches))
	discarded := make([]exactMatch, 0, len(matches))
	for index, match := range matches {
		if keep[index] {
			kept = append(kept, match)
		} else {
			discarded = append(discarded, match)
		}
	}
	return kept, discarded
}

func checkFilterContext(ctx context.Context, operation int) error {
	if operation&filterContextMask == 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func filterFalsePositiveMatches(
	ctx context.Context,
	engine *matchEngine,
	matches []exactMatch,
) ([]exactMatch, error) {
	kept := make([]exactMatch, 0, len(matches))
	for index, match := range matches {
		if err := checkFilterContext(ctx, index); err != nil {
			return nil, err
		}
		if engine.rules[match.ruleIndex].Flags&corpus.FlagFalsePositive == 0 {
			kept = append(kept, match)
		}
	}
	return kept, nil
}

func matchOverlap(first, second exactMatch) int {
	start := max(first.tokenStart, second.tokenStart)
	end := min(first.tokenEnd, second.tokenEnd)
	return max(0, end-start)
}

func compareExactMatches(first, second exactMatch) int {
	if compared := cmp.Compare(first.tokenStart, second.tokenStart); compared != 0 {
		return compared
	}
	if compared := cmp.Compare(second.length(), first.length()); compared != 0 {
		return compared
	}
	return cmp.Compare(first.ruleIndex, second.ruleIndex)
}

func sortExactMatches(engine *matchEngine, matches []exactMatch) {
	slices.SortStableFunc(matches, func(first, second exactMatch) int {
		if compared := cmp.Compare(first.tokenStart, second.tokenStart); compared != 0 {
			return compared
		}
		if compared := cmp.Compare(first.tokenEnd, second.tokenEnd); compared != 0 {
			return compared
		}
		return cmp.Compare(
			engine.rules[first.ruleIndex].ID,
			engine.rules[second.ruleIndex].ID,
		)
	})
}
