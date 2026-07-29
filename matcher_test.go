package licenses

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/git-pkgs/licenses/internal/aho"
	"github.com/git-pkgs/licenses/internal/corpus"
	"github.com/git-pkgs/licenses/internal/tokenize"
)

func TestMatcherWholeTextHash(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, true)
	input := []byte("  ALPHA\tbeta!  ")
	result, err := matcher.Match(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Corpus.Version != "test" || result.Corpus.SourceCommit != "test-commit" {
		t.Fatalf("corpus = %#v", result.Corpus)
	}
	if len(result.Detections) != 1 {
		t.Fatalf("detections = %#v", result.Detections)
	}
	detection := result.Detections[0]
	if detection.Expression != "AGPL-3.0 OR MIT" {
		t.Fatalf("expression = %q", detection.Expression)
	}
	if len(detection.Matches) != 1 {
		t.Fatalf("matches = %#v", detection.Matches)
	}
	match := detection.Matches[0]
	if match.Method != Hash || match.Start != 2 || match.End != 12 {
		t.Fatalf("match = %#v", match)
	}
	if match.Score != 85 || match.Coverage != 100 {
		t.Fatalf("score and coverage = %v, %v", match.Score, match.Coverage)
	}
	if match.Kind != KindText {
		t.Fatalf("kind = %q, want %q", match.Kind, KindText)
	}
	if !slices.Equal(match.LicenseIDs, []string{"AGPL-3.0", "MIT"}) {
		t.Fatalf("license IDs = %#v", match.LicenseIDs)
	}
	if string(match.Matched) != "ALPHA\tbeta" {
		t.Fatalf("matched text = %q", match.Matched)
	}
}

func TestMatcherCorpus(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, false)
	if got := matcher.Corpus(); got.Version != "test" ||
		got.RuleCount != len(matcher.engine.rules) ||
		got.SourceCommit != "test-commit" {
		t.Fatalf("corpus = %#v", got)
	}
	var nilMatcher *Matcher
	if got := nilMatcher.Corpus(); got != (CorpusInfo{}) {
		t.Fatalf("nil matcher corpus = %#v", got)
	}
}

func TestMatcherAhoExactMatchesAndClues(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, true)
	input := []byte("clue prefix alpha-beta suffix")
	result, err := matcher.Match(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Detections) != 1 {
		t.Fatalf("detections = %#v", result.Detections)
	}
	if result.Detections[0].Expression != "AGPL-3.0 OR MIT" {
		t.Fatalf("first detection = %#v", result.Detections[0])
	}
	full := result.Detections[0].Matches[0]
	if full.Method != Exact || full.Start != 12 || full.End != 22 || string(full.Matched) != "alpha-beta" {
		t.Fatalf("full match = %#v", full)
	}
	if len(result.Clues) != 1 || result.Clues[0].RuleID != "a-clue.RULE" {
		t.Fatalf("clues = %#v", result.Clues)
	}
	if result.Clues[0].Kind != KindClue {
		t.Fatalf("clue kind = %q, want %q", result.Clues[0].Kind, KindClue)
	}
}

func TestRuleKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		flags uint16
		want  Kind
	}{
		{flags: corpus.FlagLicenseText, want: KindText},
		{flags: corpus.FlagLicenseNotice, want: KindNotice},
		{flags: corpus.FlagLicenseTag, want: KindTag},
		{flags: corpus.FlagLicenseReference, want: KindReference},
		{flags: corpus.FlagLicenseIntro, want: KindIntro},
		{flags: corpus.FlagLicenseClue, want: KindClue},
		{
			flags: corpus.FlagLicenseText | corpus.FlagLicenseNotice,
			want:  KindUnknown,
		},
		{want: KindUnknown},
	}
	for _, test := range tests {
		if got := ruleKind(test.flags); got != test.want {
			t.Errorf("ruleKind(%d) = %q, want %q", test.flags, got, test.want)
		}
	}
}

func TestEmbeddedCorpusHasNoMatchableUnknownRuleKinds(t *testing.T) {
	matcher, err := New()
	if err != nil {
		t.Fatal(err)
	}
	var unknown []string
	for _, rule := range matcher.engine.rules {
		if rule.Flags&corpus.FlagFalsePositive == 0 &&
			ruleKind(rule.Flags) == KindUnknown {
			unknown = append(unknown, rule.ID)
		}
	}
	if len(unknown) != 0 {
		t.Fatalf("matchable rules with unknown kinds: %v", unknown)
	}
}

func TestMatcherSuppressesFalsePositiveRules(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, false)
	result, err := matcher.Match(context.Background(), []byte("prefix reject suffix"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Detections) != 0 || len(result.Clues) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestMatcherKeepsRequiredPhraseRulesWhenMatchedExactly(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, false)
	result, err := matcher.Match(context.Background(), []byte("prefix phrase suffix"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Detections) != 1 ||
		result.Detections[0].Expression != "required-helper" {
		t.Fatalf("detections = %#v", result.Detections)
	}
}

func TestMatcherReturnsContextError(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := matcher.Match(ctx, []byte("alpha beta")); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestCollectExactMatchesEnforcesCandidateLimit(t *testing.T) {
	t.Parallel()

	const token = 1
	rules := []corpus.Rule{
		{Tokens: []uint32{token}},
		{Tokens: []uint32{token}},
	}
	automaton, err := aho.Build([]aho.Pattern{
		{Tokens: rules[0].Tokens, Value: 0},
		{Tokens: rules[1].Tokens, Value: 1},
	}, len(rules))
	if err != nil {
		t.Fatal(err)
	}
	engine := matchEngine{rules: rules, automaton: automaton}
	tokens := make([]tokenize.ID, maxExactMatchCandidates/len(rules)+1)
	for index := range tokens {
		tokens[index] = token
	}

	_, err = engine.collectExactMatches(context.Background(), tokens)
	if err == nil {
		t.Fatal("collectExactMatches accepted too many candidates")
	}
	if !errors.Is(err, ErrTooManyMatches) {
		t.Fatalf("error = %v, want ErrTooManyMatches", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprint(maxExactMatchCandidates)) {
		t.Fatalf("error %q does not name limit %d", err, maxExactMatchCandidates)
	}
}

func TestEmbeddedMatcherCapsRepeatedCandidates(t *testing.T) {
	matcher, err := New()
	if err != nil {
		t.Fatal(err)
	}
	input := []byte(strings.Repeat("mit license ", 100_000))

	_, err = matcher.Match(context.Background(), input)
	if err == nil {
		t.Fatal("Match accepted too many candidates")
	}
	if !errors.Is(err, ErrTooManyMatches) {
		t.Fatalf("error = %v, want ErrTooManyMatches", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprint(maxExactMatchCandidates)) {
		t.Fatalf("error %q does not name limit %d", err, maxExactMatchCandidates)
	}
}

func TestMatcherIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, false)
	const workers = 32
	var wait sync.WaitGroup
	errors := make(chan error, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := matcher.Match(context.Background(), []byte("prefix alpha beta suffix"))
			if err != nil {
				errors <- err
				return
			}
			if len(result.Detections) != 1 {
				errors <- fmt.Errorf("detection count = %d, want 1", len(result.Detections))
			}
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestEmbeddedMatcherMatchesCorpusRule(t *testing.T) {
	matcher, err := New(WithMatchedText())
	if err != nil {
		t.Fatal(err)
	}
	ruleIndex := -1
	for index, rule := range matcher.engine.rules {
		if rule.ID == "mit.LICENSE" {
			ruleIndex = index
			break
		}
	}
	if ruleIndex < 0 {
		t.Fatal("mit.LICENSE is absent")
	}
	input := normalizedRuleText(matcher.engine, matcher.engine.rules[ruleIndex])
	result, err := matcher.Match(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Detections) == 0 {
		t.Fatal("MIT license was not detected")
	}
	var found bool
	for _, detection := range result.Detections {
		for _, match := range detection.Matches {
			if match.RuleID == "mit.LICENSE" && match.Method == Hash {
				found = true
				if !slices.Equal(match.Matched, input) {
					t.Fatalf("matched text differs from input")
				}
			}
		}
	}
	if !found {
		t.Fatalf("MIT hash match absent: %#v", result.Detections)
	}
}

func TestEmbeddedMatcherPreservesSeparateLicenseSections(t *testing.T) {
	matcher, err := New()
	if err != nil {
		t.Fatal(err)
	}
	mit := embeddedRuleText(t, matcher, "mit.LICENSE")
	apache := embeddedRuleText(t, matcher, "apache-2.0.LICENSE")
	bsd := embeddedRuleText(t, matcher, "bsd-new.LICENSE")

	tests := []struct {
		name        string
		input       []byte
		expressions []string
	}{
		{
			name: "two complete license texts",
			input: bytes.Join(
				[][]byte{mit, []byte("\ncomponent boundary\n"), bsd},
				nil,
			),
			expressions: []string{"mit", "bsd-new"},
		},
		{
			name: "Apache text with appended MIT section",
			input: bytes.Join(
				[][]byte{apache, []byte("\nadditional component terms\n"), mit},
				nil,
			),
			expressions: []string{"apache-2.0", "mit"},
		},
		{
			name: "BSD text embedded in a larger document",
			input: bytes.Join(
				[][]byte{
					[]byte("project license begins\n"),
					bsd,
					[]byte("\nproject license ends"),
				},
				nil,
			),
			expressions: []string{"bsd-new"},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			result, err := matcher.Match(context.Background(), test.input)
			if err != nil {
				t.Fatal(err)
			}
			for _, expression := range test.expressions {
				if !slices.Contains(resultExpressions(result), expression) {
					t.Fatalf(
						"expression %q absent from detections %#v",
						expression,
						result.Detections,
					)
				}
			}
		})
	}
}

func embeddedRuleText(t *testing.T, matcher *Matcher, ruleID string) []byte {
	t.Helper()

	for _, rule := range matcher.engine.rules {
		if rule.ID == ruleID {
			return normalizedRuleText(matcher.engine, rule)
		}
	}
	t.Fatalf("%s is absent", ruleID)
	return nil
}

func resultExpressions(result Result) []string {
	expressions := make([]string, 0, len(result.Detections))
	for _, detection := range result.Detections {
		expressions = append(expressions, detection.Expression)
	}
	return expressions
}

func testMatcher(t *testing.T, matchedText bool) *Matcher {
	t.Helper()

	words := []string{"alpha", "beta", "clue", "phrase", "reject"}
	vocabulary, err := tokenize.NewVocabularyFromWords(words)
	if err != nil {
		t.Fatal(err)
	}
	tokenIDs := func(text string) []uint32 {
		t.Helper()
		tokens := vocabulary.Tokenize([]byte(text))
		ids := make([]uint32, len(tokens.IDs))
		for index, id := range tokens.IDs {
			ids[index] = uint32(id)
		}
		return ids
	}
	rules := []corpus.Rule{
		{
			ID:         "a-clue.RULE",
			Expression: "license-clue",
			Tokens:     tokenIDs("clue"),
			Flags:      corpus.FlagLicenseClue,
			Relevance:  100,
		},
		{
			ID:     "b-false-positive.RULE",
			Tokens: tokenIDs("reject"),
			Flags:  corpus.FlagFalsePositive,
		},
		{
			ID:         "c-full.RULE",
			Expression: "AGPL-3.0 OR MIT",
			Tokens:     tokenIDs("alpha beta"),
			Flags:      corpus.FlagLicenseText,
			Relevance:  85,
		},
		{
			ID:         "d-required.RULE",
			Expression: "required-helper",
			Tokens:     tokenIDs("phrase"),
			Flags:      corpus.FlagRequiredPhrase,
			Relevance:  100,
		},
		{
			ID:         "e-suffix.RULE",
			Expression: "MIT",
			Tokens:     tokenIDs("beta"),
			Flags:      corpus.FlagLicenseNotice,
			Relevance:  100,
		},
	}
	patterns := make([]aho.Pattern, len(rules))
	for index := range rules {
		patterns[index] = aho.Pattern{Tokens: rules[index].Tokens, Value: uint32(index)}
	}
	automaton, err := aho.Build(patterns, len(rules))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := newMatchEngine(corpus.Index{
		Info: corpus.Info{
			Version:      "test",
			RuleCount:    len(rules),
			SourceCommit: "test-commit",
		},
		Vocabulary: words,
		Rules:      rules,
		Automaton:  automaton,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Matcher{engine: engine, matchedText: matchedText}
}
