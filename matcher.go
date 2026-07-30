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
	// Expression is copied exactly from the ScanCode rule.
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
	// LicenseIDs contains identifiers copied from the rule expression.
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
	rules                  []corpus.Rule
	ruleExpressionMetadata []uint32
	expressionMetadata     []expressionMetadata
	automaton              aho.Automaton
	hashes                 map[uint64][]uint32
}

type expressionMetadata struct {
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
	metadataIndexes := make(map[string]uint32)
	ruleMetadata := make([]uint32, len(index.Rules))
	var metadata []expressionMetadata
	for ruleIndex, rule := range index.Rules {
		metadataIndex, exists := metadataIndexes[rule.Expression]
		if !exists {
			identifiers := expressionIDs(rule.Expression)
			metadataIndex = uint32(len(metadata))
			metadataIndexes[rule.Expression] = metadataIndex
			metadata = append(metadata, expressionMetadata{
				licenseIDs:     identifiers,
				identification: identificationForIDs(identifiers),
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
		rules:                  index.Rules,
		ruleExpressionMetadata: ruleMetadata,
		expressionMetadata:     metadata,
		automaton:              index.Automaton,
		hashes:                 hashes,
	}, nil
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

	tokens := m.engine.vocabulary.Tokenize(b)
	result := Result{Corpus: m.engine.info}
	if len(tokens.IDs) == 0 {
		return result, nil
	}
	candidates, err := m.engine.collectExactMatches(ctx, tokens.IDs)
	if err != nil {
		return Result{}, err
	}
	candidates, err = filterExactMatches(ctx, m.engine, candidates, filters)
	if err != nil {
		return Result{}, err
	}
	for _, candidate := range candidates {
		rule := m.engine.rules[candidate.ruleIndex]
		metadata := m.engine.metadataForRule(candidate.ruleIndex)
		start := tokens.Offsets[candidate.tokenStart].Start
		end := tokens.Offsets[candidate.tokenEnd-1].End
		addMatch(
			&result,
			rule,
			metadata.identification,
			m.makeMatch(b, rule, metadata, candidate.method, start, end),
		)
	}
	sortResult(&result)
	return result, nil
}

const maxExactMatchCandidates = 100_000

func (e *matchEngine) collectExactMatches(
	ctx context.Context,
	tokens []tokenize.ID,
) ([]exactMatch, error) {
	if matches := e.hashMatches(tokens); len(matches) != 0 {
		return matches, nil
	}

	var candidates []exactMatch
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
			ruleLength := len(e.rules[ruleIndex].Tokens)
			if ruleLength > position+1 {
				continue
			}
			if len(candidates) == maxExactMatchCandidates {
				return nil, exactMatchCandidateLimitError()
			}
			candidates = append(candidates, exactMatch{
				ruleIndex:  ruleIndex,
				method:     Exact,
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
	matches := make([]exactMatch, 0, len(candidates))
	for _, ruleIndex := range candidates {
		if equalTokens(tokens, e.rules[ruleIndex].Tokens) {
			matches = append(matches, exactMatch{
				ruleIndex: ruleIndex,
				method:    Hash,
				tokenEnd:  len(tokens),
			})
		}
	}
	return matches
}

func equalTokens(input []tokenize.ID, rule []uint32) bool {
	if len(input) != len(rule) {
		return false
	}
	for index, token := range input {
		if uint32(token) != rule[index] {
			return false
		}
	}
	return true
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
	rule corpus.Rule,
	metadata expressionMetadata,
	method Method,
	start int,
	end int,
) Match {
	match := Match{
		RuleID:     rule.ID,
		LicenseIDs: slices.Clone(metadata.licenseIDs),
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

func addMatch(
	result *Result,
	rule corpus.Rule,
	identification Identification,
	match Match,
) {
	if rule.Flags&corpus.FlagLicenseClue != 0 {
		result.Clues = append(result.Clues, match)
		return
	}
	for index := range result.Detections {
		if result.Detections[index].Expression == rule.Expression {
			result.Detections[index].Matches = append(result.Detections[index].Matches, match)
			return
		}
	}
	result.Detections = append(result.Detections, Detection{
		Expression:     rule.Expression,
		Identification: identification,
		Matches:        []Match{match},
	})
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
	fields := strings.FieldsFunc(expression, func(character rune) bool {
		return character == '(' || character == ')' || character == ' ' ||
			character == '\t' || character == '\r' || character == '\n'
	})
	ids := make([]string, 0, len(fields))
	for _, field := range fields {
		switch strings.ToUpper(field) {
		case "AND", "OR", "WITH":
			continue
		}
		if !slices.Contains(ids, field) {
			ids = append(ids, field)
		}
	}
	return ids
}

func sortResult(result *Result) {
	for index := range result.Detections {
		sortMatches(result.Detections[index].Matches)
	}
	slices.SortFunc(result.Detections, func(first, second Detection) int {
		firstStart := first.Matches[0].Start
		secondStart := second.Matches[0].Start
		if compared := cmp.Compare(firstStart, secondStart); compared != 0 {
			return compared
		}
		return cmp.Compare(first.Expression, second.Expression)
	})
	sortMatches(result.Clues)
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
