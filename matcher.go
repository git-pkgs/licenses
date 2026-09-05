package licenses

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/git-pkgs/licenses/internal/aho"
	"github.com/git-pkgs/licenses/internal/corpus"
	"github.com/git-pkgs/licenses/internal/tokenize"
)

// Method identifies the matching stage that produced a match.
type Method string

const (
	// Hash is a normalized whole-text hash match.
	Hash Method = "hash"
	// Exact is an exact token-sequence match within a larger input.
	Exact Method = "exact"
	// SpdxID is a strictly parsed SPDX-License-Identifier tag match.
	SpdxID Method = "spdx-id"
)

// Kind identifies the ScanCode category of a matched rule.
type Kind string

const (
	// KindUnknown identifies a rule without an is_license_* category.
	KindUnknown Kind = "unknown"
	// KindText identifies a full license text rule.
	KindText Kind = "text"
	// KindNotice identifies a license notice rule.
	KindNotice Kind = "notice"
	// KindTag identifies a license tag rule.
	KindTag Kind = "tag"
	// KindReference identifies a license reference rule.
	KindReference Kind = "reference"
	// KindIntro identifies a license introduction rule.
	KindIntro Kind = "intro"
	// KindClue identifies a weak license clue rule.
	KindClue Kind = "clue"
)

// Identification states whether a detected expression names concrete
// licenses.
type Identification string

const (
	// Identified means the expression contains no ScanCode placeholder
	// identifiers.
	Identified Identification = "identified"
	// Partial means the expression contains both non-placeholder and ScanCode
	// placeholder identifiers.
	Partial Identification = "partial"
	// NoAssertion uses SPDX's NOASSERTION term when the expression contains
	// only ScanCode placeholder identifiers.
	NoAssertion Identification = "NOASSERTION"
)

// ErrTooManyMatches is returned when an input produces more exact-match
// candidates than the matcher can safely filter.
var ErrTooManyMatches = errors.New("licenses: too many exact-match candidates")

// Result contains conclusive detections and weaker clue matches.
type Result struct {
	Detections []Detection
	Clues      []Match
	Corpus     CorpusInfo
}

// Detection groups matches that state the same license expression.
type Detection struct {
	// Expression uses canonical SPDX identifiers where available and
	// LicenseRef-scancode-* identifiers for other ScanCode license keys.
	Expression string
	// Identification is derived from the identifiers in Expression.
	Identification Identification
	// Matches contains the rule matches that state Expression.
	Matches []Match
}

// Match describes one rule match in the input.
type Match struct {
	// RuleID is the ScanCode rule identifier.
	RuleID string
	// LicenseIDs contains the SPDX-compatible identifiers in the expression.
	LicenseIDs []string
	// Kind identifies the ScanCode category of the matched rule.
	Kind Kind
	// Method identifies the exact matching stage that produced the match.
	Method Method
	// Score is the rule's 0-100 relevance, not a similarity score.
	Score float64
	// Coverage is 100 for every exact match.
	Coverage float64
	// Start is the inclusive byte offset into the input.
	Start int
	// End is the exclusive byte offset into the input.
	End int
	// Matched is a copy of input[Start:End] when WithMatchedText is set.
	// It is nil otherwise.
	Matched []byte
}

// Option configures a Matcher.
type Option func(*matcherOptions)

type matcherOptions struct {
	matchedText bool
}

// WithMatchedText includes a copy of each matched input range in Match.Matched.
func WithMatchedText() Option {
	return func(options *matcherOptions) {
		options.matchedText = true
	}
}

// Matcher matches byte slices against an immutable embedded corpus.
type Matcher struct {
	engine      *matchEngine
	matchedText bool
}

// Corpus returns information about the embedded corpus used by m. It returns
// the zero value for a nil or uninitialized Matcher.
func (m *Matcher) Corpus() CorpusInfo {
	if m == nil || m.engine == nil {
		return CorpusInfo{}
	}
	return m.engine.info
}

type matchEngine struct {
	info                   CorpusInfo
	vocabulary             *tokenize.Vocabulary
	rules                  []matchRule
	ruleTokenLengths       []uint32
	ruleExpressionMetadata []uint32
	expressionMetadata     []expressionMetadata
	automaton              aho.Automaton
	hashes                 map[uint64][]uint32
	spdx                   spdxIndex
}

// matchRule contains only the rule data needed after matcher construction.
type matchRule struct {
	ID         string
	Expression string
	Flags      uint16
	Relevance  uint8
}

type expressionMetadata struct {
	expression     string
	licenseIDs     []string
	identification Identification
}

var (
	embeddedEngine     *matchEngine
	embeddedEngineErr  error
	embeddedEngineOnce sync.Once
)

// New loads the embedded corpus. The decoded corpus is shared by every Matcher
// in the process.
func New(options ...Option) (*Matcher, error) {
	var config matcherOptions
	for _, option := range options {
		if option == nil {
			return nil, errors.New("licenses: nil option")
		}
		option(&config)
	}

	embeddedEngineOnce.Do(func() {
		index, err := corpus.Load()
		if err != nil {
			embeddedEngineErr = err
			return
		}
		embeddedEngine, embeddedEngineErr = newMatchEngine(index)
	})
	if embeddedEngineErr != nil {
		return nil, embeddedEngineErr
	}
	return &Matcher{
		engine:      embeddedEngine,
		matchedText: config.matchedText,
	}, nil
}

func newMatchEngine(index corpus.Index) (*matchEngine, error) {
	vocabulary, err := tokenize.NewVocabularyFromWords(index.Vocabulary)
	if err != nil {
		return nil, err
	}
	hashes := make(map[uint64][]uint32, len(index.Rules))
	spdxIndex := buildSPDXIndex(index)
	metadataIndexes := make(map[string]uint32)
	rules := make([]matchRule, len(index.Rules))
	ruleTokenLengths := make([]uint32, len(index.Rules))
	ruleMetadata := make([]uint32, len(index.Rules))
	var metadata []expressionMetadata
	for ruleIndex, rule := range index.Rules {
		// Foundation attribution alone does not identify an Apache license version.
		switch rule.ID {
		case "apache-2.0_required_phrase_31.RULE", "apache-2.0_required_phrase_42.RULE":
			rule.Flags &^= corpus.FlagLicenseReference
			rule.Flags |= corpus.FlagLicenseClue
		}
		rules[ruleIndex] = matchRule{
			ID:         rule.ID,
			Expression: rule.Expression,
			Flags:      rule.Flags,
			Relevance:  rule.Relevance,
		}
		ruleTokenLengths[ruleIndex] = uint32(len(rule.Tokens))
		metadataIndex, exists := metadataIndexes[rule.Expression]
		if !exists {
			scanCodeIDs := expressionIDs(rule.Expression)
			expression, identifiers := spdxIndex.reportExpression(rule.Expression)
			metadataIndex = uint32(len(metadata))
			metadataIndexes[rule.Expression] = metadataIndex
			metadata = append(metadata, expressionMetadata{
				expression:     expression,
				licenseIDs:     identifiers,
				identification: identificationForIDs(scanCodeIDs),
			})
		}
		ruleMetadata[ruleIndex] = metadataIndex
		if len(rule.Tokens) == 0 {
			continue
		}
		hash := hashRuleTokens(rule.Tokens)
		hashes[hash] = append(hashes[hash], uint32(ruleIndex))
	}
	return &matchEngine{
		info: CorpusInfo{
			Version:      index.Info.Version,
			RuleCount:    index.Info.RuleCount,
			SourceCommit: index.Info.SourceCommit,
		},
		vocabulary:             vocabulary,
		rules:                  rules,
		ruleTokenLengths:       ruleTokenLengths,
		ruleExpressionMetadata: ruleMetadata,
		expressionMetadata:     metadata,
		automaton:              index.Automaton,
		hashes:                 hashes,
		spdx:                   spdxIndex,
	}, nil
}

// matchScratch holds per-call buffers reused across Match invocations.
type matchScratch struct {
	ids     []tokenize.ID
	offsets []tokenize.Offset
	word    []byte
}

// Pooled buffers are capped so a single large input does not retain memory
// for every subsequent call. The cap is on capacity, not length.
const (
	matchScratchTokenCap = 1 << 16
	matchScratchWordCap  = 1 << 12
)

var matchScratchPool = sync.Pool{
	New: func() any { return &matchScratch{} },
}

func getMatchScratch() *matchScratch {
	return matchScratchPool.Get().(*matchScratch)
}

func putMatchScratch(s *matchScratch) {
	s.dropOversized()
	matchScratchPool.Put(s)
}

func (s *matchScratch) dropOversized() {
	if cap(s.ids) > matchScratchTokenCap {
		s.ids = nil
	}
	if cap(s.offsets) > matchScratchTokenCap {
		s.offsets = nil
	}
	if cap(s.word) > matchScratchWordCap {
		s.word = nil
	}
}

// Match finds exact normalized rule matches in b. It returns
// ErrTooManyMatches when the input exceeds the exact-match candidate limit;
// callers can identify it with errors.Is.
func (m *Matcher) Match(ctx context.Context, b []byte) (Result, error) {
	return m.match(ctx, b, allExactFilters)
}

func (m *Matcher) match(ctx context.Context, b []byte, filters exactFilterOptions) (Result, error) {
	if m == nil || m.engine == nil {
		return Result{}, errors.New("licenses: nil matcher")
	}
	if ctx == nil {
		return Result{}, errors.New("licenses: nil context")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	scratch := getMatchScratch()
	defer putMatchScratch(scratch)

	tokens := m.engine.vocabulary.TokenizeIDsAppend(b, scratch.ids, &scratch.word)
	scratch.ids = tokens.IDs
	result := Result{Corpus: m.engine.info}
	if len(tokens.IDs) == 0 {
		m.matchSPDXTags(b, &result)
		sortResult(&result)
		return result, nil
	}
	candidates, method, err := m.engine.collectExactMatches(ctx, tokens.IDs)
	if err != nil {
		return Result{}, err
	}
	candidates, err = filterExactMatches(ctx, m.engine, candidates, filters)
	if err != nil {
		return Result{}, err
	}
	var offsets []tokenize.Offset
	if len(candidates) != 0 && method == Exact {
		offsets = tokenize.TokenOffsetsAppend(b, scratch.offsets)
		scratch.offsets = offsets
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
	}
	if err := m.addExactMatches(
		ctx,
		b,
		&result,
		candidates,
		method,
		offsets,
		tokens.Start,
		tokens.End,
	); err != nil {
		return Result{}, err
	}
	changed := downgradeContinuedText(b, &result)
	if m.matchSPDXTags(b, &result) || changed {
		sortResult(&result)
	} else {
		sortDetections(result.Detections)
	}
	return result, nil
}

const maxExactMatchCandidates = 100_000

// Large candidate sets use two full automaton passes so the retained slice is
// allocated once at its exact, bounded size.
const exactMatchTwoPassThreshold = 4096

func (e *matchEngine) collectExactMatches(
	ctx context.Context,
	tokens []tokenize.ID,
) ([]exactMatch, Method, error) {
	if matches := e.hashMatches(tokens); len(matches) != 0 {
		return matches, Hash, nil
	}

	var candidates []exactMatch
	var outputs []uint32
	state := uint32(0)
	for position, token := range tokens {
		if position&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, "", err
			}
		}
		state = e.automaton.Next(state, uint32(token))
		outputs = e.automaton.AppendOutputs(outputs[:0], state)
		for _, ruleIndex := range outputs {
			ruleLength := int(e.ruleTokenLengths[ruleIndex])
			if ruleLength > position+1 {
				continue
			}
			if len(candidates) == maxExactMatchCandidates {
				return nil, "", exactMatchCandidateLimitError()
			}
			candidates = append(candidates, exactMatch{
				ruleIndex:  ruleIndex,
				tokenStart: position + 1 - ruleLength,
				tokenEnd:   position + 1,
			})
			if len(candidates) == exactMatchTwoPassThreshold {
				matches, err := e.collectManyExactMatches(ctx, tokens)
				return matches, Exact, err
			}
		}
	}
	return candidates, Exact, nil
}

func (e *matchEngine) collectManyExactMatches(
	ctx context.Context,
	tokens []tokenize.ID,
) ([]exactMatch, error) {
	count := 0
	var outputs []uint32
	state := uint32(0)
	for position, token := range tokens {
		if position&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		state = e.automaton.Next(state, uint32(token))
		outputs = e.automaton.AppendOutputs(outputs[:0], state)
		for _, ruleIndex := range outputs {
			if int(e.ruleTokenLengths[ruleIndex]) > position+1 {
				continue
			}
			if count == maxExactMatchCandidates {
				return nil, exactMatchCandidateLimitError()
			}
			count++
		}
	}

	candidates := make([]exactMatch, 0, count)
	outputs = outputs[:0]
	state = 0
	for position, token := range tokens {
		if position&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		state = e.automaton.Next(state, uint32(token))
		outputs = e.automaton.AppendOutputs(outputs[:0], state)
		for _, ruleIndex := range outputs {
			ruleLength := int(e.ruleTokenLengths[ruleIndex])
			if ruleLength > position+1 {
				continue
			}
			candidates = append(candidates, exactMatch{
				ruleIndex:  ruleIndex,
				tokenStart: position + 1 - ruleLength,
				tokenEnd:   position + 1,
			})
		}
	}
	return candidates, nil
}

func exactMatchCandidateLimitError() error {
	return fmt.Errorf(
		"%w: limit %d",
		ErrTooManyMatches,
		maxExactMatchCandidates,
	)
}

func (e *matchEngine) hashMatches(tokens []tokenize.ID) []exactMatch {
	candidates := e.hashes[hashInputTokens(tokens)]
	if len(candidates) == 0 {
		return nil
	}
	state := uint32(0)
	for _, token := range tokens {
		state = e.automaton.Next(state, uint32(token))
	}
	matches := make([]exactMatch, 0, len(candidates))
	for _, ruleIndex := range candidates {
		if int(e.ruleTokenLengths[ruleIndex]) == len(tokens) &&
			e.automaton.HasOutput(state, ruleIndex) {
			matches = append(matches, exactMatch{
				ruleIndex: ruleIndex,
				tokenEnd:  len(tokens),
			})
		}
	}
	return matches
}

const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
	fullScore   = 100
)

func hashInputTokens(tokens []tokenize.ID) uint64 {
	hash := uint64(fnvOffset64)
	for _, token := range tokens {
		hash = hashToken(hash, uint32(token))
	}
	return hash
}

func hashRuleTokens(tokens []uint32) uint64 {
	hash := uint64(fnvOffset64)
	for _, token := range tokens {
		hash = hashToken(hash, token)
	}
	return hash
}

func hashToken(hash uint64, token uint32) uint64 {
	for range 4 {
		hash ^= uint64(byte(token))
		hash *= fnvPrime64
		token >>= 8
	}
	return hash
}

func (e *matchEngine) metadataForRule(ruleIndex uint32) expressionMetadata {
	return e.expressionMetadata[e.ruleExpressionMetadata[ruleIndex]]
}

func (m *Matcher) makeMatch(
	input []byte,
	rule matchRule,
	licenseIDs []string,
	method Method,
	start int,
	end int,
) Match {
	match := Match{
		RuleID:     rule.ID,
		LicenseIDs: licenseIDs,
		Kind:       ruleKind(rule.Flags),
		Method:     method,
		Score:      float64(rule.Relevance),
		Coverage:   fullScore,
		Start:      start,
		End:        end,
	}
	if m.matchedText {
		match.Matched = slices.Clone(input[start:end])
	}
	return match
}

func (m *Matcher) addExactMatches(
	ctx context.Context,
	input []byte,
	result *Result,
	candidates []exactMatch,
	method Method,
	offsets []tokenize.Offset,
	firstTokenStart int,
	lastTokenEnd int,
) error {
	if len(candidates) == 0 {
		return nil
	}

	detectionCounts := make(map[string]int)
	clueCount := 0
	identifierCount := 0
	for index, candidate := range candidates {
		if err := checkFilterContext(ctx, index); err != nil {
			return err
		}
		rule := m.engine.rules[candidate.ruleIndex]
		metadata := m.engine.metadataForRule(candidate.ruleIndex)
		identifierCount += len(metadata.licenseIDs)
		if rule.Flags&corpus.FlagLicenseClue != 0 {
			clueCount++
			continue
		}
		detectionCounts[metadata.expression]++
	}

	if len(detectionCounts) != 0 {
		result.Detections = make([]Detection, 0, len(detectionCounts))
	}
	if clueCount != 0 {
		result.Clues = make([]Match, 0, clueCount)
	}
	var identifiers []string
	if identifierCount != 0 {
		identifiers = make([]string, identifierCount)
	}
	for index, candidate := range candidates {
		if err := checkFilterContext(ctx, index); err != nil {
			return err
		}
		rule := m.engine.rules[candidate.ruleIndex]
		metadata := m.engine.metadataForRule(candidate.ruleIndex)
		var licenseIDs []string
		if len(metadata.licenseIDs) == 0 {
			licenseIDs = slices.Clone(metadata.licenseIDs)
		} else {
			licenseIDs = identifiers[:len(metadata.licenseIDs):len(metadata.licenseIDs)]
			copy(licenseIDs, metadata.licenseIDs)
			identifiers = identifiers[len(licenseIDs):]
		}

		start, end := firstTokenStart, lastTokenEnd
		if method == Exact {
			start = offsets[candidate.tokenStart].Start
			end = offsets[candidate.tokenEnd-1].End
		}
		match := m.makeMatch(input, rule, licenseIDs, method, start, end)
		if rule.Flags&corpus.FlagLicenseClue != 0 {
			result.Clues = append(result.Clues, match)
			continue
		}

		count := detectionCounts[metadata.expression]
		var detectionIndex int
		if count > 0 {
			detectionIndex = len(result.Detections)
			result.Detections = append(result.Detections, Detection{
				Expression:     metadata.expression,
				Identification: metadata.identification,
				Matches:        make([]Match, 0, count),
			})
			detectionCounts[metadata.expression] = -detectionIndex - 1
		} else {
			detectionIndex = -count - 1
		}
		result.Detections[detectionIndex].Matches = append(
			result.Detections[detectionIndex].Matches,
			match,
		)
	}
	return nil
}

func ruleKind(flags uint16) Kind {
	const mask = corpus.FlagLicenseText |
		corpus.FlagLicenseNotice |
		corpus.FlagLicenseTag |
		corpus.FlagLicenseReference |
		corpus.FlagLicenseIntro |
		corpus.FlagLicenseClue

	switch flags & mask {
	case corpus.FlagLicenseText:
		return KindText
	case corpus.FlagLicenseNotice:
		return KindNotice
	case corpus.FlagLicenseTag:
		return KindTag
	case corpus.FlagLicenseReference:
		return KindReference
	case corpus.FlagLicenseIntro:
		return KindIntro
	case corpus.FlagLicenseClue:
		return KindClue
	default:
		return KindUnknown
	}
}

func identificationForIDs(identifiers []string) Identification {
	if len(identifiers) == 0 {
		return NoAssertion
	}
	var concrete, placeholder bool
	for _, identifier := range identifiers {
		if isPlaceholderIdentifier(identifier) {
			placeholder = true
		} else {
			concrete = true
		}
	}
	switch {
	case concrete && placeholder:
		return Partial
	case placeholder:
		return NoAssertion
	default:
		return Identified
	}
}

func isPlaceholderIdentifier(identifier string) bool {
	switch strings.ToLower(identifier) {
	case "free-unknown",
		"generic-cla",
		"generic-exception",
		"generic-export-compliance",
		"generic-tos",
		"generic-trademark",
		"other-copyleft",
		"other-permissive",
		"public-domain-disclaimer",
		"see-license",
		"unknown",
		"unknown-license-reference",
		"unknown-spdx",
		"warranty-disclaimer":
		return true
	default:
		return false
	}
}

func expressionIDs(expression string) []string {
	var identifiers []string
	rewriteExpressionIdentifiers(expression, func(identifier string) string {
		if !slices.Contains(identifiers, identifier) {
			identifiers = append(identifiers, identifier)
		}
		return identifier
	})
	return identifiers
}

func rewriteExpressionIdentifiers(
	expression string,
	rewrite func(string) string,
) string {
	var result strings.Builder
	result.Grow(len(expression))
	for offset := 0; offset < len(expression); {
		if isExpressionSeparator(expression[offset]) {
			result.WriteByte(expression[offset])
			offset++
			continue
		}
		end := offset + 1
		for end < len(expression) && !isExpressionSeparator(expression[end]) {
			end++
		}
		token := expression[offset:end]
		switch strings.ToUpper(token) {
		case "AND", "OR", "WITH":
			result.WriteString(token)
		default:
			result.WriteString(rewrite(token))
		}
		offset = end
	}
	return result.String()
}

func isExpressionSeparator(character byte) bool {
	return character == '(' || character == ')' || character == ' ' ||
		character == '\t' || character == '\r' || character == '\n'
}

func sortResult(result *Result) {
	for index := range result.Detections {
		sortMatches(result.Detections[index].Matches)
	}
	sortDetections(result.Detections)
	sortMatches(result.Clues)
}

func sortDetections(detections []Detection) {
	slices.SortFunc(detections, func(first, second Detection) int {
		firstStart := first.Matches[0].Start
		secondStart := second.Matches[0].Start
		if compared := cmp.Compare(firstStart, secondStart); compared != 0 {
			return compared
		}
		return cmp.Compare(first.Expression, second.Expression)
	})
}

func sortMatches(matches []Match) {
	slices.SortFunc(matches, func(first, second Match) int {
		if compared := cmp.Compare(first.Start, second.Start); compared != 0 {
			return compared
		}
		if compared := cmp.Compare(first.End, second.End); compared != 0 {
			return compared
		}
		if compared := cmp.Compare(first.RuleID, second.RuleID); compared != 0 {
			return compared
		}
		return cmp.Compare(first.Method, second.Method)
	})
}
