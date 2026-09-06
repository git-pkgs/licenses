package licenses

import (
	"cmp"
	"context"
	"slices"

	"github.com/git-pkgs/licenses/internal/corpus"
)

const (
	overlapMedium                   = 0.40
	overlapLarge                    = 0.70
	overlapExtraLarge               = 0.90
	minimumMatchCount               = 2
	filterContextMask               = 4095
	minimumLicenseListMatches       = 15
	minimumLicenseListExpressions   = 5
	maximumLicenseListMatchTokens   = 20
	maximumLicenseListMatchDistance = 10
	minimumLicenseListInputCoverage = 0.50
)

type exactMatch struct {
	ruleIndex  uint32
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

type exactMatchState uint8

const (
	exactMatchActive exactMatchState = iota
	exactMatchDiscardedContained
	exactMatchDiscardedOverlapping
	exactMatchDiscardedFinal
	exactMatchRestored
)

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

	states := make([]exactMatchState, len(matches))
	var discardedContained, discardedOverlapping bool
	if options.contained {
		var err error
		discardedContained, err = filterContainedMatches(
			ctx,
			matches,
			states,
			exactMatchDiscardedContained,
		)
		if err != nil {
			return nil, err
		}
	}
	if options.overlapping {
		var err error
		discardedOverlapping, err = filterOverlappingMatches(
			ctx,
			engine,
			matches,
			states,
		)
		if err != nil {
			return nil, err
		}
	}
	if discardedContained {
		if err := restoreNonOverlapping(
			ctx,
			matches,
			states,
			exactMatchDiscardedContained,
		); err != nil {
			return nil, err
		}
	}
	if discardedOverlapping {
		if err := restoreNonOverlapping(
			ctx,
			matches,
			states,
			exactMatchDiscardedOverlapping,
		); err != nil {
			return nil, err
		}
	}
	if options.contained {
		if _, err := filterContainedMatches(
			ctx,
			matches,
			states,
			exactMatchDiscardedFinal,
		); err != nil {
			return nil, err
		}
	}
	if options.falsePositive {
		if err := filterFalsePositiveMatches(ctx, engine, matches, states); err != nil {
			return nil, err
		}
	}

	matches = compactExactMatches(matches, states)
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
	states []exactMatchState,
	discardState exactMatchState,
) (bool, error) {
	if len(matches) < minimumMatchCount {
		return false, nil
	}

	discarded := false
	maxEnd := -1
	for index, match := range matches {
		if err := checkFilterContext(ctx, index); err != nil {
			return false, err
		}
		if states[index] != exactMatchActive {
			continue
		}
		if match.tokenEnd <= maxEnd {
			states[index] = discardState
			discarded = true
			continue
		}
		maxEnd = match.tokenEnd
	}
	return discarded, nil
}

// matches must be ordered by compareExactMatches.
func filterOverlappingMatches(
	ctx context.Context,
	engine *matchEngine,
	matches []exactMatch,
	states []exactMatchState,
) (bool, error) {
	if len(matches) < minimumMatchCount {
		return false, nil
	}

	discarded := false
	previousKept := -1
	operations := 0
	for current := 0; current < len(matches)-1; current++ {
		if states[current] != exactMatchActive {
			continue
		}
		if err := checkFilterContext(ctx, operations); err != nil {
			return false, err
		}
		operations++
		for next := current + 1; next < len(matches); next++ {
			if states[next] != exactMatchActive {
				continue
			}
			if err := checkFilterContext(ctx, operations); err != nil {
				return false, err
			}
			operations++
			if matches[next].tokenStart >= matches[current].tokenEnd {
				break
			}
			switch overlapRemovalForPair(engine, matches, current, next, previousKept) {
			case removeNext:
				states[next] = exactMatchDiscardedOverlapping
				discarded = true
				continue
			case removeCurrent:
				states[current] = exactMatchDiscardedOverlapping
				discarded = true
			}
			if states[current] != exactMatchActive {
				break
			}
		}
		if states[current] == exactMatchActive {
			previousKept = current
		}
	}
	return discarded, nil
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
	currentRule, nextRule matchRule,
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

// matches must be ordered by compareExactMatches.
func restoreNonOverlapping(
	ctx context.Context,
	matches []exactMatch,
	states []exactMatchState,
	discardState exactMatchState,
) error {
	position := 0
	union, hasUnion, err := nextActiveMatchUnion(ctx, matches, states, &position)
	if err != nil {
		return err
	}
	for index, candidate := range matches {
		if err := checkFilterContext(ctx, index); err != nil {
			return err
		}
		if states[index] != discardState {
			continue
		}
		for hasUnion && union.tokenEnd <= candidate.tokenStart {
			union, hasUnion, err = nextActiveMatchUnion(ctx, matches, states, &position)
			if err != nil {
				return err
			}
		}
		if hasUnion && union.tokenStart < candidate.tokenEnd {
			continue
		}
		states[index] = exactMatchRestored
	}
	for index, state := range states {
		if state == exactMatchRestored {
			states[index] = exactMatchActive
		}
	}
	return nil
}

func nextActiveMatchUnion(
	ctx context.Context,
	matches []exactMatch,
	states []exactMatchState,
	position *int,
) (exactMatch, bool, error) {
	for *position < len(matches) && states[*position] != exactMatchActive {
		if err := checkFilterContext(ctx, *position); err != nil {
			return exactMatch{}, false, err
		}
		*position++
	}
	if *position == len(matches) {
		return exactMatch{}, false, nil
	}
	union := matches[*position]
	*position++
	for *position < len(matches) {
		if err := checkFilterContext(ctx, *position); err != nil {
			return exactMatch{}, false, err
		}
		if states[*position] != exactMatchActive {
			*position++
			continue
		}
		match := matches[*position]
		if match.tokenStart >= union.tokenEnd {
			break
		}
		if match.tokenEnd > union.tokenEnd {
			union.tokenEnd = match.tokenEnd
		}
		*position++
	}
	return union, true, nil
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
	states []exactMatchState,
) error {
	for index, match := range matches {
		if err := checkFilterContext(ctx, index); err != nil {
			return err
		}
		if states[index] != exactMatchActive {
			continue
		}
		if engine.rules[match.ruleIndex].Flags&corpus.FlagFalsePositive != 0 {
			states[index] = exactMatchDiscardedFinal
		}
	}
	return nil
}

// Dense lists of short license names describe metadata without granting a license.
func filterLicenseListMatches(
	ctx context.Context,
	engine *matchEngine,
	matches []exactMatch,
	unknownAfter, stopwordAfter []uint32,
	totalWordCount int,
) ([]exactMatch, error) {
	if len(matches) < minimumLicenseListMatches {
		return matches, nil
	}
	kept := matches[:0]
	groupStart := -1
	flushGroup := func(end int) {
		if groupStart < 0 {
			return
		}
		group := matches[groupStart:end]
		spanWords := licenseListSpanWords(group, unknownAfter, stopwordAfter)
		coverage := float64(spanWords) / float64(totalWordCount)
		if !isLicenseList(group, engine) || coverage < minimumLicenseListInputCoverage {
			kept = append(kept, group...)
		}
		groupStart = -1
	}
	var previous exactMatch
	for index, match := range matches {
		if err := checkFilterContext(ctx, index); err != nil {
			return nil, err
		}
		if !isLicenseListCandidate(match, engine) {
			flushGroup(index)
			kept = append(kept, match)
			continue
		}
		if groupStart < 0 {
			groupStart = index
		} else if licenseListMatchDistance(
			previous,
			match,
			unknownAfter,
			stopwordAfter,
		) > maximumLicenseListMatchDistance {
			flushGroup(index)
			groupStart = index
		}
		previous = match
	}
	flushGroup(len(matches))
	return kept, nil
}

func isLicenseListCandidate(match exactMatch, engine *matchEngine) bool {
	const candidateKinds = corpus.FlagLicenseReference |
		corpus.FlagLicenseTag |
		corpus.FlagLicenseIntro |
		corpus.FlagLicenseClue
	return match.length() <= maximumLicenseListMatchTokens &&
		engine.rules[match.ruleIndex].Flags&candidateKinds != 0
}

func isLicenseList(matches []exactMatch, engine *matchEngine) bool {
	if len(matches) < minimumLicenseListMatches {
		return false
	}
	expressions := make(map[string]struct{}, minimumLicenseListExpressions)
	for _, match := range matches {
		expressions[engine.rules[match.ruleIndex].Expression] = struct{}{}
	}
	return len(expressions) >= minimumLicenseListExpressions
}

func licenseListMatchDistance(
	previous, current exactMatch,
	unknownAfter, stopwordAfter []uint32,
) int {
	start := uint32(previous.tokenEnd)
	end := uint32(current.tokenStart)
	return current.tokenStart - previous.tokenEnd + 1 +
		positionsBetween(unknownAfter, start, end) +
		positionsBetween(stopwordAfter, start, end)
}

func licenseListSpanWords(
	matches []exactMatch,
	unknownAfter, stopwordAfter []uint32,
) int {
	start := uint32(matches[0].tokenStart)
	end := uint32(matches[len(matches)-1].tokenEnd)
	return int(end-start) +
		positionsWithin(unknownAfter, start, end) +
		positionsWithin(stopwordAfter, start, end)
}

func positionsBetween(positions []uint32, start, end uint32) int {
	from, _ := slices.BinarySearch(positions, start)
	to, _ := slices.BinarySearch(positions, end+1)
	return to - from
}

func positionsWithin(positions []uint32, start, end uint32) int {
	from, _ := slices.BinarySearch(positions, start+1)
	to, _ := slices.BinarySearch(positions, end)
	return to - from
}

func compactExactMatches(matches []exactMatch, states []exactMatchState) []exactMatch {
	kept := matches[:0]
	for index, match := range matches {
		if states[index] == exactMatchActive {
			kept = append(kept, match)
		}
	}
	return kept
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
