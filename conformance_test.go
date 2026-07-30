package licenses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	conformanceDataEnvironment   = "SCANCODE_TESTDATA"
	updateBaselineEnvironment    = "UPDATE_CONFORMANCE"
	reportConformanceEnvironment = "REPORT_CONFORMANCE"
	profileFiltersEnvironment    = "PROFILE_CONFORMANCE_FILTERS"
	conformanceBaselinePath      = "testdata/scancode_conformance_exact.json"
)

var conformanceSuites = []string{
	"datadriven/lic1",
	"datadriven/lic2",
	"datadriven/lic3",
	"datadriven/lic4",
}

type conformanceCase struct {
	path            string
	input           []byte
	expected        []string
	expectedFailure bool
}

type conformanceMetadata struct {
	LicenseExpressions []string `yaml:"license_expressions"`
	Language           string   `yaml:"language"`
	Notes              string   `yaml:"notes"`
	ExpectedFailure    bool     `yaml:"expected_failure"`
}

type conformanceBaseline struct {
	SourceCommit string                  `json:"source_commit"`
	Mode         string                  `json:"mode"`
	Cases        int                     `json:"cases"`
	Evaluated    int                     `json:"evaluated"`
	Passed       int                     `json:"passed"`
	Skipped      []conformanceSkip       `json:"skipped,omitempty"`
	Divergences  []conformanceDivergence `json:"divergences"`
}

type conformanceSkip struct {
	Path string `json:"path"`
	Note string `json:"note"`
}

type conformanceDivergence struct {
	Path          string   `json:"path"`
	Expected      []string `json:"expected,omitempty"`
	Actual        []string `json:"actual,omitempty"`
	ExactExpected []string `json:"exact_expected,omitempty"`
	Stage         string   `json:"stage"`
	Note          string   `json:"note"`
}

type conformanceOutcome struct {
	path          string
	expected      []string
	actual        []string
	exactExpected []string
	skipped       bool
}

func TestScanCodeConformanceExact(t *testing.T) {
	dataRoot := os.Getenv(conformanceDataEnvironment)
	if dataRoot == "" {
		t.Skipf("set %s to the pinned ScanCode tests/licensedcode/data directory", conformanceDataEnvironment)
	}

	cases, matcher := loadConformanceMatcher(t, dataRoot)
	if os.Getenv(profileFiltersEnvironment) == "1" {
		profileConformanceFilters(t, matcher, cases)
		return
	}
	outcomes := runConformanceCases(t, matcher, cases, allExactFilters)
	current := buildConformanceBaseline(matcher.engine.info.SourceCommit, outcomes)
	if os.Getenv(reportConformanceEnvironment) == "1" {
		reportConformance(t, matcher, cases, current)
		return
	}
	if os.Getenv(updateBaselineEnvironment) == "1" {
		updateConformanceBaseline(t, current)
		return
	}
	checkConformanceBaseline(t, current)
}

func loadConformanceMatcher(t *testing.T, dataRoot string) ([]conformanceCase, *Matcher) {
	t.Helper()

	cases, err := loadConformanceCases(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	matcher, err := New()
	if err != nil {
		t.Fatal(err)
	}
	pinnedCommit, err := pinnedCorpusCommit("CORPUS_VERSION")
	if err != nil {
		t.Fatal(err)
	}
	if pinnedCommit != matcher.engine.info.SourceCommit {
		t.Fatalf(
			"CORPUS_VERSION pins %s, embedded corpus reports %s",
			pinnedCommit,
			matcher.engine.info.SourceCommit,
		)
	}
	checkoutCommit, err := scanCodeCheckoutCommit(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if checkoutCommit != matcher.engine.info.SourceCommit {
		t.Fatalf(
			"ScanCode test data is from %s, embedded corpus is from %s",
			checkoutCommit,
			matcher.engine.info.SourceCommit,
		)
	}
	return cases, matcher
}

func reportConformance(
	t *testing.T,
	matcher *Matcher,
	cases []conformanceCase,
	current conformanceBaseline,
) {
	t.Helper()

	baseline, err := readConformanceBaseline(conformanceBaselinePath)
	if err != nil {
		t.Fatal(err)
	}
	improved, regressed := conformancePassChanges(baseline, current)
	t.Logf(
		"exact conformance report: %d/%d passed (%.2f%%), %d improved, %d regressed, %d skipped",
		current.Passed,
		current.Evaluated,
		conformanceRate(current),
		len(improved),
		len(regressed),
		len(current.Skipped),
	)
	if len(regressed) == 0 {
		return
	}
	t.Logf("regressed cases: %s", strings.Join(regressed, ", "))
	for _, divergence := range current.Divergences {
		if !slices.Contains(regressed, divergence.Path) {
			continue
		}
		t.Logf(
			"%s expected %v, actual %v",
			divergence.Path,
			divergence.Expected,
			divergence.Actual,
		)
		for _, testCase := range cases {
			if testCase.path == divergence.Path {
				logConformanceMatchTrace(t, matcher, testCase)
				break
			}
		}
	}
}

func updateConformanceBaseline(t *testing.T, current conformanceBaseline) {
	t.Helper()

	if err := writeConformanceBaseline(conformanceBaselinePath, current); err != nil {
		t.Fatal(err)
	}
	t.Logf(
		"updated exact baseline: %d/%d passed (%.2f%%), %d skipped",
		current.Passed,
		current.Evaluated,
		conformanceRate(current),
		len(current.Skipped),
	)
}

func checkConformanceBaseline(t *testing.T, current conformanceBaseline) {
	t.Helper()

	baseline, err := readConformanceBaseline(conformanceBaselinePath)
	if err != nil {
		t.Fatal(err)
	}
	problems := compareConformanceBaseline(baseline, current)
	if len(problems) != 0 {
		t.Fatalf("conformance baseline regressed:\n%s", strings.Join(problems, "\n"))
	}
	t.Logf(
		"exact conformance: %d/%d passed (%.2f%%), baseline %d/%d, %d skipped",
		current.Passed,
		current.Evaluated,
		conformanceRate(current),
		baseline.Passed,
		baseline.Evaluated,
		len(current.Skipped),
	)
}

func logConformanceMatchTrace(t *testing.T, matcher *Matcher, testCase conformanceCase) {
	t.Helper()

	tokenized := matcher.engine.vocabulary.Tokenize(testCase.input)
	raw, err := matcher.engine.collectExactMatches(context.Background(), tokenized.IDs)
	if err != nil {
		t.Fatalf("%s: trace exact matches: %v", testCase.path, err)
	}
	filtered, err := filterExactMatches(
		context.Background(),
		matcher.engine,
		raw,
		allExactFilters,
	)
	if err != nil {
		t.Fatalf("%s: filter exact matches: %v", testCase.path, err)
	}
	for _, match := range raw {
		rule := matcher.engine.rules[match.ruleIndex]
		if !slices.Contains(testCase.expected, rule.Expression) && match.length() < 100 {
			continue
		}
		t.Logf(
			"%s exact candidate: rule=%s expression=%s span=%d:%d length=%d kept=%t",
			testCase.path,
			rule.ID,
			rule.Expression,
			match.tokenStart,
			match.tokenEnd,
			match.length(),
			slices.ContainsFunc(filtered, func(filteredMatch exactMatch) bool {
				return exactMatchesEqual(filteredMatch, match)
			}),
		)
	}
}

func exactMatchesEqual(first, second exactMatch) bool {
	return first.ruleIndex == second.ruleIndex &&
		first.method == second.method &&
		first.tokenStart == second.tokenStart &&
		first.tokenEnd == second.tokenEnd
}

func conformancePassChanges(
	baseline,
	current conformanceBaseline,
) (improved, regressed []string) {
	previousDivergences := make(map[string]struct{}, len(baseline.Divergences))
	for _, divergence := range baseline.Divergences {
		previousDivergences[divergence.Path] = struct{}{}
	}
	currentDivergences := make(map[string]struct{}, len(current.Divergences))
	for _, divergence := range current.Divergences {
		currentDivergences[divergence.Path] = struct{}{}
	}
	for path := range previousDivergences {
		if _, remains := currentDivergences[path]; !remains {
			improved = append(improved, path)
		}
	}
	for path := range currentDivergences {
		if _, wasKnown := previousDivergences[path]; !wasKnown {
			regressed = append(regressed, path)
		}
	}
	sort.Strings(improved)
	sort.Strings(regressed)
	return improved, regressed
}

func pinnedCorpusCommit(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), "commit="); found {
			return strings.TrimSpace(value), nil
		}
	}
	return "", fmt.Errorf("%s has no commit", path)
}

func scanCodeCheckoutCommit(dataRoot string) (string, error) {
	command := exec.Command("git", "-C", dataRoot, "rev-parse", "HEAD")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("read ScanCode test-data commit: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func loadConformanceCases(root string) ([]conformanceCase, error) {
	var cases []conformanceCase
	for _, suite := range conformanceSuites {
		suiteRoot := filepath.Join(root, filepath.FromSlash(suite))
		err := filepath.WalkDir(suiteRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".yml" {
				return nil
			}
			testCase, err := loadConformanceCase(root, path)
			if err != nil {
				return err
			}
			cases = append(cases, testCase)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("load ScanCode suite %s: %w", suite, err)
		}
	}
	sort.Slice(cases, func(first, second int) bool {
		return cases[first].path < cases[second].path
	})
	return cases, nil
}

func loadConformanceCase(root, metadataPath string) (conformanceCase, error) {
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		return conformanceCase{}, err
	}
	var metadata conformanceMetadata
	decoder := yaml.NewDecoder(bytes.NewReader(metadataBytes))
	decoder.KnownFields(true)
	if err := decoder.Decode(&metadata); err != nil {
		return conformanceCase{}, fmt.Errorf("%s: %w", metadataPath, err)
	}
	inputPath := strings.TrimSuffix(metadataPath, ".yml")
	input, err := os.ReadFile(inputPath)
	if err != nil {
		return conformanceCase{}, err
	}
	relativePath, err := filepath.Rel(root, inputPath)
	if err != nil {
		return conformanceCase{}, err
	}
	return conformanceCase{
		path:            filepath.ToSlash(relativePath),
		input:           input,
		expected:        uniqueExpressions(metadata.LicenseExpressions),
		expectedFailure: metadata.ExpectedFailure,
	}, nil
}

func profileConformanceFilters(t *testing.T, matcher *Matcher, cases []conformanceCase) {
	t.Helper()

	profiles := []struct {
		name    string
		filters exactFilterOptions
	}{
		{name: "raw"},
		{
			name: "contained",
			filters: exactFilterOptions{
				contained: true,
			},
		},
		{
			name: "overlapping",
			filters: exactFilterOptions{
				contained:   true,
				overlapping: true,
			},
		},
		{name: "false positives", filters: allExactFilters},
	}
	for _, profile := range profiles {
		outcomes := runConformanceCases(t, matcher, cases, profile.filters)
		baseline := buildConformanceBaseline(matcher.engine.info.SourceCommit, outcomes)
		t.Logf(
			"%s: %d/%d passed (%.2f%%)",
			profile.name,
			baseline.Passed,
			baseline.Evaluated,
			conformanceRate(baseline),
		)
		counts := make(map[string]int)
		for _, divergence := range baseline.Divergences {
			counts[divergence.Stage]++
		}
		stages := make([]string, 0, len(counts))
		for stage, count := range counts {
			stages = append(stages, fmt.Sprintf("%s=%d", stage, count))
		}
		sort.Strings(stages)
		t.Logf("%s stages: %s", profile.name, strings.Join(stages, ", "))
	}
}

func runConformanceCases(
	t *testing.T,
	matcher *Matcher,
	cases []conformanceCase,
	filters exactFilterOptions,
) []conformanceOutcome {
	t.Helper()

	outcomes := make([]conformanceOutcome, 0, len(cases))
	for _, testCase := range cases {
		outcome := conformanceOutcome{
			path:     testCase.path,
			expected: testCase.expected,
			skipped:  testCase.expectedFailure,
		}
		if !testCase.expectedFailure {
			result, err := matcher.match(context.Background(), testCase.input, filters)
			if err != nil {
				t.Fatalf("%s: %v", testCase.path, err)
			}
			expressions := make([]string, 0, len(result.Detections))
			for _, detection := range result.Detections {
				expressions = append(expressions, detection.Expression)
			}
			outcome.actual = uniqueExpressions(expressions)
			outcome.exactExpected = expressionIntersection(
				testCase.expected,
				rawExactExpressions(matcher.engine, testCase.input),
			)
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}

func rawExactExpressions(engine *matchEngine, input []byte) []string {
	tokens := engine.vocabulary.Tokenize(input)
	if len(tokens.IDs) == 0 {
		return nil
	}
	if ruleIndexes := engine.hashMatches(tokens.IDs); len(ruleIndexes) != 0 {
		expressions := make([]string, 0, len(ruleIndexes))
		for _, match := range ruleIndexes {
			expressions = append(
				expressions,
				engine.rules[match.ruleIndex].Expression,
			)
		}
		return uniqueExpressions(expressions)
	}

	var expressions []string
	var outputs []uint32
	state := uint32(0)
	for _, token := range tokens.IDs {
		state = engine.automaton.Next(state, uint32(token))
		outputs = engine.automaton.AppendOutputs(outputs[:0], state)
		for _, ruleIndex := range outputs {
			expressions = append(expressions, engine.rules[ruleIndex].Expression)
		}
	}
	return uniqueExpressions(expressions)
}

func uniqueExpressions(expressions []string) []string {
	unique := make([]string, 0, len(expressions))
	for _, expression := range expressions {
		if expression != "" && !slices.Contains(unique, expression) {
			unique = append(unique, expression)
		}
	}
	sort.Strings(unique)
	return unique
}

func buildConformanceBaseline(sourceCommit string, outcomes []conformanceOutcome) conformanceBaseline {
	baseline := conformanceBaseline{
		SourceCommit: sourceCommit,
		Mode:         "exact-core expressions, duplicates and order ignored; known divergences are won't-fix",
		Cases:        len(outcomes),
	}
	for _, outcome := range outcomes {
		if outcome.skipped {
			baseline.Skipped = append(baseline.Skipped, conformanceSkip{
				Path: outcome.path,
				Note: "ScanCode marks this case as an expected failure",
			})
			continue
		}
		baseline.Evaluated++
		if slices.Equal(outcome.actual, outcome.expected) {
			baseline.Passed++
			continue
		}
		baseline.Divergences = append(baseline.Divergences, conformanceDivergence{
			Path:          outcome.path,
			Expected:      outcome.expected,
			Actual:        outcome.actual,
			ExactExpected: outcome.exactExpected,
			Stage: conformanceDivergenceStage(
				outcome.expected,
				outcome.actual,
				outcome.exactExpected,
			),
			Note: conformanceDivergenceNote(
				outcome.expected,
				outcome.actual,
				outcome.exactExpected,
			),
		})
	}
	return baseline
}

const (
	divergenceFilterSuppressed = "filter-suppressed"
	divergenceMixed            = "mixed"
	divergenceNoExactMatch     = "no-exact-match"
	divergenceWrongExactSet    = "wrong-exact-rule-set"
	divergencePartialExactSet  = "partial-exact-rule-set"
	divergenceFilterRetained   = "filter-retained"
)

func conformanceDivergenceStage(expected, actual, exactExpected []string) string {
	missing := expressionDifference(expected, actual)
	unexpected := expressionDifference(actual, expected)
	exactButMissing := expressionIntersection(missing, exactExpected)
	switch {
	case len(missing) != 0 && len(exactButMissing) == len(missing):
		return divergenceFilterSuppressed
	case len(exactButMissing) != 0:
		return divergenceMixed
	case len(actual) == 0:
		return divergenceNoExactMatch
	case len(missing) != 0 && len(unexpected) != 0:
		return divergenceWrongExactSet
	case len(missing) != 0:
		return divergencePartialExactSet
	default:
		return divergenceFilterRetained
	}
}

func conformanceDivergenceNote(expected, actual, exactExpected []string) string {
	missing := expressionDifference(expected, actual)
	unexpected := expressionDifference(actual, expected)
	exactButMissing := expressionIntersection(missing, exactExpected)
	switch conformanceDivergenceStage(expected, actual, exactExpected) {
	case divergenceFilterSuppressed:
		return fmt.Sprintf(
			"won't-fix exact core: the filter suppressed %d expected expression(s) that matched exactly",
			len(missing),
		)
	case divergenceMixed:
		return fmt.Sprintf(
			"won't-fix exact core: %d missing expression(s) matched exactly and %d had no exact match",
			len(exactButMissing),
			len(missing)-len(exactButMissing),
		)
	case divergenceNoExactMatch:
		return fmt.Sprintf(
			"won't-fix exact core: no exact match for %d expected expression(s)",
			len(missing),
		)
	case divergenceWrongExactSet:
		return fmt.Sprintf(
			"won't-fix exact core: %d expected expression(s) missing and %d unexpected exact expression(s) present",
			len(missing),
			len(unexpected),
		)
	case divergencePartialExactSet:
		return fmt.Sprintf(
			"won't-fix exact core: %d expected expression(s) had no exact match",
			len(missing),
		)
	default:
		return fmt.Sprintf(
			"won't-fix exact core: the filter retained %d unexpected expression(s)",
			len(unexpected),
		)
	}
}

func expressionIntersection(first, second []string) []string {
	intersection := make([]string, 0, min(len(first), len(second)))
	for _, expression := range first {
		if slices.Contains(second, expression) {
			intersection = append(intersection, expression)
		}
	}
	return intersection
}

func expressionDifference(first, second []string) []string {
	difference := make([]string, 0, len(first))
	for _, expression := range first {
		if !slices.Contains(second, expression) {
			difference = append(difference, expression)
		}
	}
	return difference
}

func writeConformanceBaseline(path string, baseline conformanceBaseline) error {
	data, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func readConformanceBaseline(path string) (conformanceBaseline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return conformanceBaseline{}, err
	}
	var baseline conformanceBaseline
	if err := json.Unmarshal(data, &baseline); err != nil {
		return conformanceBaseline{}, err
	}
	return baseline, nil
}

func compareConformanceBaseline(baseline, current conformanceBaseline) []string {
	var problems []string
	if current.SourceCommit != baseline.SourceCommit {
		problems = append(problems, fmt.Sprintf(
			"source commit changed from %s to %s",
			baseline.SourceCommit,
			current.SourceCommit,
		))
	}
	if current.Mode != baseline.Mode {
		problems = append(problems, fmt.Sprintf(
			"comparison mode changed from %q to %q",
			baseline.Mode,
			current.Mode,
		))
	}
	if current.Cases != baseline.Cases || current.Evaluated != baseline.Evaluated {
		problems = append(problems, fmt.Sprintf(
			"suite size changed from %d/%d to %d/%d cases/evaluated",
			baseline.Cases,
			baseline.Evaluated,
			current.Cases,
			current.Evaluated,
		))
	}
	if current.Passed < baseline.Passed {
		problems = append(problems, fmt.Sprintf(
			"pass count fell from %d to %d",
			baseline.Passed,
			current.Passed,
		))
	}

	known := make(map[string]conformanceDivergence, len(baseline.Divergences))
	for _, divergence := range baseline.Divergences {
		known[divergence.Path] = divergence
	}
	for _, divergence := range current.Divergences {
		previous, exists := known[divergence.Path]
		if !exists {
			problems = append(problems, "new divergence: "+divergence.Path)
			continue
		}
		delete(known, divergence.Path)
		if !slices.Equal(divergence.Expected, previous.Expected) ||
			!slices.Equal(divergence.Actual, previous.Actual) ||
			!slices.Equal(divergence.ExactExpected, previous.ExactExpected) ||
			divergence.Stage != previous.Stage {
			problems = append(problems, "changed divergence: "+divergence.Path)
		}
	}
	sort.Strings(problems)
	const maximumProblems = 20
	if len(problems) > maximumProblems {
		problems = append(problems[:maximumProblems], "additional conformance problems omitted")
	}
	return problems
}

func conformanceRate(baseline conformanceBaseline) float64 {
	if baseline.Evaluated == 0 {
		return 0
	}
	return 100 * float64(baseline.Passed) / float64(baseline.Evaluated)
}

func TestConformanceBaselineAllowsImprovements(t *testing.T) {
	t.Parallel()

	baseline := conformanceBaseline{
		SourceCommit: "commit",
		Cases:        2,
		Evaluated:    2,
		Passed:       1,
		Divergences: []conformanceDivergence{{
			Path:     "failed",
			Expected: []string{"mit"},
			Note:     "exact stage",
		}},
	}
	current := conformanceBaseline{
		SourceCommit: "commit",
		Cases:        2,
		Evaluated:    2,
		Passed:       2,
	}
	if problems := compareConformanceBaseline(baseline, current); len(problems) != 0 {
		t.Fatalf("improvement failed: %v", problems)
	}
}

func TestConformanceBaselineRejectsChangedDivergence(t *testing.T) {
	t.Parallel()

	baseline := conformanceBaseline{
		SourceCommit: "commit",
		Cases:        1,
		Evaluated:    1,
		Divergences: []conformanceDivergence{{
			Path:     "failed",
			Expected: []string{"mit"},
			Note:     "exact stage",
		}},
	}
	current := baseline
	current.Divergences = []conformanceDivergence{{
		Path:     "failed",
		Expected: []string{"mit"},
		Actual:   []string{"apache-2.0"},
		Note:     "changed",
	}}
	if problems := compareConformanceBaseline(baseline, current); len(problems) == 0 {
		t.Fatal("changed divergence did not fail")
	}
}

func TestConformanceBaselineRejectsPassToFailDespiteHigherPassCount(t *testing.T) {
	t.Parallel()

	baseline := conformanceBaseline{
		SourceCommit: "commit",
		Cases:        3,
		Evaluated:    3,
		Passed:       1,
		Divergences: []conformanceDivergence{
			{Path: "old-failure-a"},
			{Path: "old-failure-b"},
		},
	}
	current := conformanceBaseline{
		SourceCommit: "commit",
		Cases:        3,
		Evaluated:    3,
		Passed:       2,
		Divergences: []conformanceDivergence{{
			Path: "new-failure",
		}},
	}
	problems := compareConformanceBaseline(baseline, current)
	if !slices.Contains(problems, "new divergence: new-failure") {
		t.Fatalf("problems = %v", problems)
	}
}
