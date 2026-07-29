package licenses

import (
	"slices"
	"sort"

	"github.com/git-pkgs/licenses/internal/corpus"
	"github.com/git-pkgs/licenses/internal/tokenize"
)

const (
	overlapSmall      = 0.10
	overlapMedium     = 0.40
	overlapLarge      = 0.70
	overlapExtraLarge = 0.90
	minimumMatchCount = 2
)

const (
	methodOrderHash = iota
	methodOrderExact
	methodOrderSeq
	methodOrderUnknown
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
	requiredPhrases bool
	contained       bool
	overlapping     bool
	falsePositive   bool
}

var allExactFilters = exactFilterOptions{
	requiredPhrases: true,
	contained:       true,
	overlapping:     true,
	falsePositive:   true,
}

func filterExactMatches(
	engine *matchEngine,
	matches []exactMatch,
	tokens []tokenize.ID,
	options exactFilterOptions,
) []exactMatch {
	if options.requiredPhrases {
		matches = filterMissingRequiredPhrases(engine, matches, tokens)
	}

	var discardedContained, discardedOverlapping []exactMatch
	if options.contained {
		matches, discardedContained = filterContainedMatches(matches)
	}
	if options.overlapping {
		matches, discardedOverlapping = filterOverlappingMatches(engine, matches)
	}
	if len(discardedContained) != 0 {
		restored := restoreNonOverlapping(matches, discardedContained)
		matches = append(matches, restored...)
	}
	if len(discardedOverlapping) != 0 {
		restored := restoreNonOverlapping(matches, discardedOverlapping)
		matches = append(matches, restored...)
	}
	if options.contained {
		matches, _ = filterContainedMatches(matches)
	}
	if options.falsePositive {
		matches = filterFalsePositiveMatches(engine, matches)
	}

	sortExactMatches(engine, matches)
	return matches
}

func filterMissingRequiredPhrases(
	engine *matchEngine,
	matches []exactMatch,
	tokens []tokenize.ID,
) []exactMatch {
	kept := make([]exactMatch, 0, len(matches))
	for _, match := range matches {
		phrases := engine.requiredPhrases[match.ruleIndex]
		if len(phrases) == 0 {
			kept = append(kept, match)
			continue
		}
		matchedTokens := tokens[match.tokenStart:match.tokenEnd]
		hasAll := true
		for _, phrase := range phrases {
			if len(phrase) == 0 || !containsTokenSequence(matchedTokens, phrase) {
				hasAll = false
				break
			}
		}
		if hasAll {
			kept = append(kept, match)
		}
	}
	return kept
}

func containsTokenSequence(tokens, sequence []tokenize.ID) bool {
	if len(sequence) > len(tokens) {
		return false
	}
	for start := 0; start <= len(tokens)-len(sequence); start++ {
		if slices.Equal(tokens[start:start+len(sequence)], sequence) {
			return true
		}
	}
	return false
}

func filterContainedMatches(matches []exactMatch) ([]exactMatch, []exactMatch) {
	if len(matches) < minimumMatchCount {
		return matches, nil
	}
	matches = slices.Clone(matches)
	sort.SliceStable(matches, func(first, second int) bool {
		return exactMatchOrder(matches[first], matches[second])
	})

	var discarded []exactMatch
	for current := 0; current < len(matches)-1; current++ {
		for next := current + 1; next < len(matches); {
			if matches[next].tokenEnd > matches[current].tokenEnd {
				break
			}
			discarded = append(discarded, matches[next])
			matches = slices.Delete(matches, next, next+1)
		}
	}
	return matches, discarded
}

func filterOverlappingMatches(
	engine *matchEngine,
	matches []exactMatch,
) ([]exactMatch, []exactMatch) {
	if len(matches) < minimumMatchCount {
		return matches, nil
	}
	matches = slices.Clone(matches)
	sort.SliceStable(matches, func(first, second int) bool {
		return exactMatchOrder(matches[first], matches[second])
	})

	var discarded []exactMatch
	for current := 0; current < len(matches)-1; current++ {
		for next := current + 1; next < len(matches); {
			if matches[next].tokenStart >= matches[current].tokenEnd {
				break
			}
			overlap := matchOverlap(matches[current], matches[next])
			if overlap == 0 {
				next++
				continue
			}
			currentRule := engine.rules[matches[current].ruleIndex]
			nextRule := engine.rules[matches[next].ruleIndex]
			if currentRule.Flags&corpus.FlagFalsePositive != 0 &&
				nextRule.Flags&corpus.FlagFalsePositive != 0 {
				next++
				continue
			}

			remove := overlappingMatchToRemove(currentRule, nextRule, matches[current], matches[next], overlap)
			switch remove {
			case removeNext:
				discarded = append(discarded, matches[next])
				matches = slices.Delete(matches, next, next+1)
				continue
			case removeCurrent:
				discarded = append(discarded, matches[current])
				matches = slices.Delete(matches, current, current+1)
				current--
				next = len(matches)
				continue
			}

			if current > 0 {
				previous := matches[current-1]
				if matchOverlap(previous, matches[next]) == 0 {
					combinedOverlap := matchOverlap(matches[current], previous) +
						matchOverlap(matches[current], matches[next])
					if float64(combinedOverlap) >= float64(matches[current].length())*overlapExtraLarge {
						discarded = append(discarded, matches[current])
						matches = slices.Delete(matches, current, current+1)
						current--
						next = len(matches)
						continue
					}
				}
			}
			next++
		}
	}
	return matches, discarded
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

func restoreNonOverlapping(
	matches, discarded []exactMatch,
) []exactMatch {
	var restored []exactMatch
	for _, candidate := range discarded {
		intersects := false
		for _, match := range matches {
			if matchOverlap(candidate, match) != 0 {
				intersects = true
				break
			}
		}
		if intersects {
			continue
		}
		restored = append(restored, candidate)
	}
	return restored
}

func filterFalsePositiveMatches(engine *matchEngine, matches []exactMatch) []exactMatch {
	kept := make([]exactMatch, 0, len(matches))
	for _, match := range matches {
		if engine.rules[match.ruleIndex].Flags&corpus.FlagFalsePositive == 0 {
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

func exactMatchOrder(first, second exactMatch) bool {
	if first.tokenStart != second.tokenStart {
		return first.tokenStart < second.tokenStart
	}
	if first.length() != second.length() {
		return first.length() > second.length()
	}
	if first.method != second.method {
		return methodOrder(first.method) < methodOrder(second.method)
	}
	return first.ruleIndex < second.ruleIndex
}

func sortExactMatches(engine *matchEngine, matches []exactMatch) {
	sort.SliceStable(matches, func(first, second int) bool {
		if matches[first].tokenStart != matches[second].tokenStart {
			return matches[first].tokenStart < matches[second].tokenStart
		}
		if matches[first].tokenEnd != matches[second].tokenEnd {
			return matches[first].tokenEnd < matches[second].tokenEnd
		}
		return engine.rules[matches[first].ruleIndex].ID <
			engine.rules[matches[second].ruleIndex].ID
	})
}

func methodOrder(method Method) int {
	switch method {
	case Hash:
		return methodOrderHash
	case Exact:
		return methodOrderExact
	case Seq:
		return methodOrderSeq
	default:
		return methodOrderUnknown
	}
}
