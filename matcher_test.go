package licenses

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
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
	if result.Clues != nil {
		t.Fatalf("clues = %#v, want nil", result.Clues)
	}
	detection := result.Detections[0]
	if detection.Expression != "AGPL-3.0 OR MIT" {
		t.Fatalf("expression = %q", detection.Expression)
	}
	if detection.Identification != Identified {
		t.Fatalf(
			"identification = %q, want %q",
			detection.Identification,
			Identified,
		)
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

func TestHashMatchesVerifiesCollisionsWithoutRuleTokens(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, false)
	if len(matcher.engine.ruleTokenLengths) != len(matcher.engine.rules) {
		t.Fatalf(
			"token lengths = %d, rules = %d",
			len(matcher.engine.ruleTokenLengths),
			len(matcher.engine.rules),
		)
	}
	tokens := matcher.engine.vocabulary.TokenizeIDs([]byte("alpha beta")).IDs
	hash := hashInputTokens(tokens)
	matcher.engine.hashes[hash] = []uint32{
		5, // Same length, different tokens.
		4, // Matching suffix, different length.
		2, // Exact token sequence.
	}
	got := matcher.engine.hashMatches(tokens)
	want := []exactMatch{{ruleIndex: 2, tokenEnd: len(tokens)}}
	if !slices.Equal(got, want) {
		t.Fatalf("hash matches = %#v, want %#v", got, want)
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

func TestMatcherAhoExactMatchIgnoresUnknownWords(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, true)
	input := []byte("prefix alpha projectname beta suffix")
	result, err := matcher.Match(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Detections) != 1 || len(result.Detections[0].Matches) != 1 {
		t.Fatalf("detections = %#v", result.Detections)
	}
	match := result.Detections[0].Matches[0]
	if match.RuleID != "c-full.RULE" || match.Method != Exact ||
		match.Start != 7 || match.End != 29 ||
		string(match.Matched) != "alpha projectname beta" {
		t.Fatalf("match = %#v", match)
	}
}

func TestMatcherAhoNoticeDoesNotSpanUnknownWords(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, false)
	result, err := matcher.Match(context.Background(), []byte("alpha projectname reject"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Detections) != 0 || len(result.Clues) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestMatcherAhoLongNoticeIgnoresUnknownWords(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, false)
	inputWords := make([]string, minimumVariableNoticeTokens+1)
	for index := range inputWords {
		inputWords[index] = "alpha"
	}
	inputWords[minimumVariableNoticeTokens/2] = "projectname"
	result, err := matcher.Match(context.Background(), []byte(strings.Join(inputWords, " ")))
	if err != nil {
		t.Fatal(err)
	}
	if got := resultExpressions(result); !slices.Contains(got, "LicenseRef-scancode-mpl-2.0") {
		t.Fatalf("expressions = %v, want LicenseRef-scancode-mpl-2.0", got)
	}
}

func TestMatcherAhoContinuousTextDoesNotSpanUnknownWords(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, false)
	matcher.engine.rules[2].Flags |= corpus.FlagContinuous
	result, err := matcher.Match(context.Background(), []byte("alpha projectname beta"))
	if err != nil {
		t.Fatal(err)
	}
	if got := resultExpressions(result); !slices.Equal(got, []string{"MIT"}) {
		t.Fatalf("expressions = %v, want [MIT]", got)
	}
}

func TestMatcherClueOnlyResultKeepsNilDetections(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, false)
	result, err := matcher.Match(context.Background(), []byte("prefix clue suffix"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Detections != nil {
		t.Fatalf("detections = %#v, want nil", result.Detections)
	}
	if len(result.Clues) != 1 {
		t.Fatalf("clues = %#v", result.Clues)
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

func TestIdentificationForExpressionIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		expression string
		want       Identification
	}{
		{want: NoAssertion},
		{expression: "MIT", want: Identified},
		{
			expression: "GPL-2.0-only WITH Classpath-exception-2.0",
			want:       Identified,
		},
		{expression: "unknown-license-reference", want: NoAssertion},
		{expression: "free-unknown OR unknown", want: NoAssertion},
		{expression: "unknown-spdx OR see-license", want: NoAssertion},
		{expression: "other-permissive", want: NoAssertion},
		{expression: "other-copyleft", want: NoAssertion},
		{expression: "warranty-disclaimer", want: NoAssertion},
		{expression: "generic-cla", want: NoAssertion},
		{expression: "generic-amiwm", want: Identified},
		{expression: "patent-disclaimer", want: Identified},
		{expression: "proprietary-license", want: Identified},
		{expression: "commercial-license", want: Identified},
		{expression: "MIT AND free-unknown", want: Partial},
		{expression: "MIT AND other-permissive", want: Partial},
	}
	for _, test := range tests {
		identifiers := expressionIDs(test.expression)
		if got := identificationForIDs(identifiers); got != test.want {
			t.Errorf(
				"identificationForIDs(expressionIDs(%q)) = %q, want %q",
				test.expression,
				got,
				test.want,
			)
		}
	}
}

func TestEmbeddedCorpusPlaceholderIdentifiersAreClassified(t *testing.T) {
	matcher, err := New()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"free-unknown",
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
		"warranty-disclaimer",
	}
	observed := make(map[string]bool)
	var unclassified []string
	for _, rule := range matcher.engine.rules {
		for _, identifier := range expressionIDs(rule.Expression) {
			if isPlaceholderIdentifier(identifier) {
				observed[identifier] = true
				continue
			}
			if potentialPlaceholderIdentifier(identifier) &&
				!slices.Contains(unclassified, identifier) {
				unclassified = append(unclassified, identifier)
			}
		}
	}
	slices.Sort(unclassified)
	if len(unclassified) != 0 {
		t.Errorf("unclassified placeholder-like identifiers: %v", unclassified)
	}
	got := make([]string, 0, len(observed))
	for identifier := range observed {
		got = append(got, identifier)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("placeholder identifiers = %v, want %v", got, want)
	}
}

func potentialPlaceholderIdentifier(identifier string) bool {
	return identifier == "free-unknown" ||
		identifier == "see-license" ||
		strings.HasPrefix(identifier, "unknown") ||
		strings.HasPrefix(identifier, "other-") ||
		identifier == "generic-cla" ||
		identifier == "generic-exception" ||
		identifier == "generic-export-compliance" ||
		identifier == "generic-tos" ||
		identifier == "generic-trademark" ||
		identifier == "public-domain-disclaimer" ||
		identifier == "warranty-disclaimer"
}

func TestMatcherReturnsIndependentLicenseIDSlices(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, false)
	first, err := matcher.Match(context.Background(), []byte("alpha beta"))
	if err != nil {
		t.Fatal(err)
	}
	first.Detections[0].Matches[0].LicenseIDs[0] = "changed"

	second, err := matcher.Match(context.Background(), []byte("alpha beta"))
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Detections[0].Matches[0].LicenseIDs[0]; got != "AGPL-3.0" {
		t.Fatalf("license ID = %q, want AGPL-3.0", got)
	}
}

func TestMatcherReturnsIndependentLicenseIDSlicesForEachMatch(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, false)
	result, err := matcher.Match(
		context.Background(),
		[]byte("alpha beta boundary alpha beta"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Detections) != 1 || len(result.Detections[0].Matches) != 2 {
		t.Fatalf("detections = %#v", result.Detections)
	}
	matches := result.Detections[0].Matches
	matches[0].LicenseIDs[0] = "changed"
	if got := matches[1].LicenseIDs[0]; got != "AGPL-3.0" {
		t.Fatalf("second match license ID = %q, want AGPL-3.0", got)
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

func TestMatcherSuppressesLicenseClassifierList(t *testing.T) {
	t.Parallel()

	input, err := os.ReadFile("testdata/license-classifiers.txt")
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := New()
	if err != nil {
		t.Fatal(err)
	}
	result, err := matcher.Match(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Detections) != 0 || len(result.Clues) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestMatcherKeepsSparseLicenseClassifierMatches(t *testing.T) {
	t.Parallel()

	input, err := os.ReadFile("testdata/license-classifiers.txt")
	if err != nil {
		t.Fatal(err)
	}
	separator := []byte("\n" + strings.Repeat("projectword ", 11))
	input = bytes.ReplaceAll(input, []byte("\n"), separator)
	matcher, err := New()
	if err != nil {
		t.Fatal(err)
	}
	result, err := matcher.Match(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Detections) == 0 && len(result.Clues) == 0 {
		t.Fatal("sparse license evidence was suppressed")
	}
}

func TestMatcherKeepsClassifierSectionInsideLongDocument(t *testing.T) {
	t.Parallel()

	classifiers, err := os.ReadFile("testdata/license-classifiers.txt")
	if err != nil {
		t.Fatal(err)
	}
	filler := []byte(strings.Repeat(" projectwordone", 500))
	matcher, err := New()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		input []byte
	}{
		{name: "leading text", input: append(slices.Clone(filler), classifiers...)},
		{name: "trailing text", input: append(slices.Clone(classifiers), filler...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := matcher.Match(context.Background(), test.input)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Detections) == 0 && len(result.Clues) == 0 {
				t.Fatal("license evidence in a longer document was suppressed")
			}
		})
	}
}

func TestMatcherKeepsRepeatedLicenseClues(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, false)
	result, err := matcher.Match(
		context.Background(),
		[]byte(strings.Repeat("clue\n", minimumLicenseListMatches)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Clues) != minimumLicenseListMatches {
		t.Fatalf("clues = %d, want %d", len(result.Clues), minimumLicenseListMatches)
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
		result.Detections[0].Expression != "LicenseRef-scancode-required-helper" {
		t.Fatalf("detections = %#v", result.Detections)
	}
}

func TestMatcherIgnoresScanCodeStopwordsOutsideContinuousRules(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, true)
	input := []byte("alpha &quot; reject")
	result, err := matcher.Match(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if expressions := resultExpressions(result); !slices.Equal(expressions, []string{"ISC"}) {
		t.Fatalf("expressions = %v", expressions)
	}
	match := result.Detections[0].Matches[0]
	if !bytes.Equal(match.Matched, input) {
		t.Fatalf("matched text = %q, want %q", match.Matched, input)
	}
}

func TestMatcherPreservesStopwordsPresentInContinuousRules(t *testing.T) {
	t.Parallel()

	matcher := testMatcher(t, true)
	input := []byte("beta &quot; reject")
	result, err := matcher.Match(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if expressions := resultExpressions(result); !slices.Equal(expressions, []string{"Apache-2.0"}) {
		t.Fatalf("expressions = %v", expressions)
	}
	match := result.Detections[0].Matches[0]
	if !bytes.Equal(match.Matched, input) {
		t.Fatalf("matched text = %q, want %q", match.Matched, input)
	}

	for _, withoutRequiredStopword := range []string{
		"beta reject",
		"beta mystery reject",
		"beta quot quot reject",
	} {
		result, err := matcher.Match(context.Background(), []byte(withoutRequiredStopword))
		if err != nil {
			t.Fatal(err)
		}
		if expressions := resultExpressions(result); !slices.Equal(expressions, []string{"MIT"}) {
			t.Fatalf("%q expressions = %v, want [MIT]", withoutRequiredStopword, expressions)
		}
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
	engine := matchEngine{
		rules:            make([]matchRule, len(rules)),
		ruleTokenLengths: []uint32{1, 1},
		automaton:        automaton,
	}
	tokens := make([]tokenize.ID, maxExactMatchCandidates/len(rules)+1)
	for index := range tokens {
		tokens[index] = token
	}

	_, _, err = engine.collectExactMatches(context.Background(), tokens)
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

func TestEmbeddedMatcherCandidateLimit(t *testing.T) {
	matcher, err := New()
	if err != nil {
		t.Fatal(err)
	}
	large := []byte(strings.Repeat("mit license ", 100_000))
	if _, err := matcher.Match(context.Background(), large); err != nil {
		t.Fatalf("Match rejected a supported large candidate set: %v", err)
	}
	input := []byte(strings.Repeat("mit license ", 400_000))

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
	input := embeddedRuleTexts(t, "mit.LICENSE")["mit.LICENSE"]
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
	texts := embeddedRuleTexts(
		t,
		"mit.LICENSE",
		"apache-2.0.LICENSE",
		"bsd-new.LICENSE",
	)
	mit := texts["mit.LICENSE"]
	apache := texts["apache-2.0.LICENSE"]
	bsd := texts["bsd-new.LICENSE"]

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
			expressions: []string{"MIT", "BSD-3-Clause"},
		},
		{
			name: "Apache text with appended MIT section",
			input: bytes.Join(
				[][]byte{apache, []byte("\nadditional component terms\n"), mit},
				nil,
			),
			expressions: []string{"Apache-2.0", "MIT"},
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
			expressions: []string{"BSD-3-Clause"},
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

func embeddedRuleTexts(t testing.TB, ruleIDs ...string) map[string][]byte {
	t.Helper()

	index, err := corpus.Load()
	if err != nil {
		t.Fatal(err)
	}
	vocabulary, err := tokenize.NewVocabularyFromWords(index.Vocabulary)
	if err != nil {
		t.Fatal(err)
	}
	wanted := make(map[string]struct{}, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		wanted[ruleID] = struct{}{}
	}
	texts := make(map[string][]byte, len(ruleIDs))
	for _, rule := range index.Rules {
		if _, ok := wanted[rule.ID]; ok {
			texts[rule.ID] = normalizedRuleText(vocabulary, rule)
		}
	}
	for _, ruleID := range ruleIDs {
		if _, ok := texts[ruleID]; !ok {
			t.Fatalf("%s is absent", ruleID)
		}
	}
	return texts
}

func normalizedRuleText(vocabulary *tokenize.Vocabulary, rule corpus.Rule) []byte {
	words := make([]string, len(rule.Tokens))
	for index, token := range rule.Tokens {
		words[index] = vocabulary.Word(tokenize.ID(token))
	}
	return []byte(strings.Join(words, " "))
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

	words := []string{"alpha", "beta", "clue", "phrase", "quot", "reject"}
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
	longNotice := strings.TrimSpace(strings.Repeat("alpha ", minimumVariableNoticeTokens))
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
		{
			ID:         "f-other.RULE",
			Expression: "ISC",
			Tokens:     tokenIDs("alpha reject"),
			Flags:      corpus.FlagLicenseNotice,
			Relevance:  100,
		},
		{
			ID:         "g-continuous.RULE",
			Expression: "BSD-2-Clause",
			Tokens:     tokenIDs("alpha reject"),
			Flags:      corpus.FlagLicenseNotice | corpus.FlagContinuous,
			Relevance:  100,
		},
		{
			ID:         "h-required-stopwords.RULE",
			Expression: "JSON",
			Tokens:     tokenIDs("alpha reject"),
			Flags:      corpus.FlagLicenseTag | corpus.FlagRequiredPhrase,
			Relevance:  100,
		},
		{
			ID:            "i-continuous-with-stopwords.RULE",
			Expression:    "Apache-2.0",
			Tokens:        tokenIDs("beta reject"),
			StopwordAfter: []uint32{1},
			Flags:         corpus.FlagLicenseNotice | corpus.FlagContinuous,
			Relevance:     100,
		},
		{
			ID:         "j-long-notice.RULE",
			Expression: "MPL-2.0",
			Tokens:     tokenIDs(longNotice),
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
		Vocabulary:  words,
		StopwordIDs: []uint32{5},
		Rules:       rules,
		Automaton:   automaton,
		ReportingIDs: map[string]string{
			"agpl-3.0":     "AGPL-3.0",
			"apache-2.0":   "Apache-2.0",
			"bsd-2-clause": "BSD-2-Clause",
			"isc":          "ISC",
			"json":         "JSON",
			"mit":          "MIT",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Matcher{engine: engine, matchedText: matchedText}
}

func TestMatchScratchRetainsBoundedBuffers(t *testing.T) {
	t.Parallel()
	small := matchScratch{
		ids:           make([]tokenize.ID, 10),
		offsets:       make([]tokenize.Offset, 10),
		unknownAfter:  make([]uint32, 10),
		stopwordAfter: make([]uint32, 10),
		word:          make([]byte, 10),
	}
	small.retain(matchScratch{})
	if cap(small.ids) != 10 || cap(small.offsets) != 10 ||
		cap(small.unknownAfter) != 10 || cap(small.stopwordAfter) != 10 ||
		cap(small.word) != 10 {
		t.Fatal("small buffers discarded")
	}
	large := matchScratch{
		ids:           make([]tokenize.ID, 0, matchScratchTokenCap+1),
		offsets:       make([]tokenize.Offset, 0, matchScratchTokenCap+1),
		unknownAfter:  make([]uint32, 0, matchScratchTokenCap+1),
		stopwordAfter: make([]uint32, 0, matchScratchTokenCap+1),
		word:          make([]byte, 0, matchScratchWordCap+1),
	}
	original := large
	large.retain(matchScratch{})
	if cap(large.ids) != matchScratchTokenCap || cap(large.offsets) != matchScratchTokenCap ||
		cap(large.unknownAfter) != matchScratchTokenCap ||
		cap(large.stopwordAfter) != matchScratchTokenCap || cap(large.word) != matchScratchWordCap {
		t.Fatal("reserve exceeds its budget")
	}
	if &large.ids[:1][0] == &original.ids[:1][0] ||
		&large.offsets[:1][0] == &original.offsets[:1][0] ||
		&large.unknownAfter[:1][0] == &original.unknownAfter[:1][0] ||
		&large.stopwordAfter[:1][0] == &original.stopwordAfter[:1][0] ||
		&large.word[:1][0] == &original.word[:1][0] {
		t.Fatal("reserve retains oversized backing array")
	}
	previous := large
	large = original
	large.retain(previous)
	if &large.ids[:1][0] != &previous.ids[:1][0] ||
		&large.offsets[:1][0] != &previous.offsets[:1][0] ||
		&large.unknownAfter[:1][0] != &previous.unknownAfter[:1][0] ||
		&large.stopwordAfter[:1][0] != &previous.stopwordAfter[:1][0] ||
		&large.word[:1][0] != &previous.word[:1][0] {
		t.Fatal("bounded reserve was reallocated")
	}
}
