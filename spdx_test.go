package licenses

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/git-pkgs/licenses/internal/corpus"
	"github.com/git-pkgs/spdx"
)

func TestMatcherSPDXTags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		expression string
		start      int
		end        int
	}{
		{
			name:       "short id",
			input:      "SPDX-License-Identifier: MIT",
			expression: "MIT",
			start:      0,
			end:        28,
		},
		{
			name:       "isc",
			input:      "SPDX-License-Identifier: ISC",
			expression: "ISC",
			start:      0,
			end:        28,
		},
		{
			name:       "line comment",
			input:      "// Copyright.\n// SPDX-License-Identifier: MIT\npackage p\n",
			expression: "MIT",
			start:      17,
			end:        45,
		},
		{
			name:       "block comment",
			input:      "/* SPDX-License-Identifier: MIT */",
			expression: "MIT",
			start:      3,
			end:        31,
		},
		{
			name:       "html comment",
			input:      "<!-- SPDX-License-Identifier: MIT -->",
			expression: "MIT",
			start:      5,
			end:        33,
		},
		{
			name:       "crlf",
			input:      "SPDX-License-Identifier: MIT\r\n",
			expression: "MIT",
			start:      0,
			end:        28,
		},
		{
			name:       "lowercase tag",
			input:      "spdx-license-identifier: mit",
			expression: "MIT",
			start:      0,
			end:        28,
		},
		{
			name:       "typo tag",
			input:      "SPDX-License-Identifer: MIT",
			expression: "MIT",
			start:      0,
			end:        27,
		},
		{
			name:       "compound without rule",
			input:      "SPDX-License-Identifier: MIT OR ISC",
			expression: "MIT OR ISC",
			start:      0,
			end:        35,
		},
		{
			name:       "unknown license ref",
			input:      "SPDX-License-Identifier: LicenseRef-Proprietary",
			expression: "LicenseRef-scancode-unknown-spdx",
			start:      0,
			end:        47,
		},
		{
			name:       "scancode license ref",
			input:      "SPDX-License-Identifier: LicenseRef-scancode-bsd-new",
			expression: "BSD-3-Clause",
			start:      0,
			end:        52,
		},
	}
	matcher := testSPDXMatcher(t)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			match, ok := findSPDXMatch(t, matcher, test.input, test.expression)
			if !ok {
				return
			}
			if match.Start != test.start || match.End != test.end {
				t.Errorf(
					"span = [%d, %d), want [%d, %d)",
					match.Start,
					match.End,
					test.start,
					test.end,
				)
			}
			if match.RuleID != spdxTagRuleID {
				t.Errorf("rule ID = %q, want %q", match.RuleID, spdxTagRuleID)
			}
			if match.Kind != KindTag {
				t.Errorf("kind = %q, want %q", match.Kind, KindTag)
			}
			if match.Method != SpdxID {
				t.Errorf("method = %q, want %q", match.Method, SpdxID)
			}
			if match.Score != fullScore || match.Coverage != fullScore {
				t.Errorf("score, coverage = %v, %v", match.Score, match.Coverage)
			}
			if string(match.Matched) != test.input[test.start:test.end] {
				t.Errorf("matched = %q", match.Matched)
			}
		})
	}
}

func TestMatcherSPDXTagsDroppedForOverlappingRuleMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		expression string
	}{
		{
			name:       "aliased id",
			input:      "SPDX-License-Identifier: BSD-3-Clause",
			expression: "BSD-3-Clause",
		},
		{
			name:       "compound rule",
			input:      "SPDX-License-Identifier: MIT OR Apache-2.0",
			expression: "MIT OR Apache-2.0",
		},
		{
			name:       "deprecated compound remap",
			input:      "/* SPDX-License-Identifier: eCos-2.0 */",
			expression: "GPL-2.0-or-later WITH eCos-exception-2.0",
		},
	}
	matcher := testSPDXMatcher(t)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := matcher.Match(context.Background(), []byte(test.input))
			if err != nil {
				t.Fatal(err)
			}
			var found bool
			for _, detection := range result.Detections {
				for _, match := range detection.Matches {
					if match.Method == SpdxID {
						t.Errorf("spdx-id match survived overlap: %#v", match)
					}
				}
				if detection.Expression == test.expression {
					found = true
				}
			}
			if !found {
				t.Errorf(
					"expression %q not detected: %#v",
					test.expression,
					result.Detections,
				)
			}
		})
	}
}

func TestMatcherSPDXTagsNoMatch(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"",
		"SPDX-License-Identifier:",
		"SPDX-License-Identifier: ",
		"SPDX-License-Identifier: \n",
		"SPDX License Identifier: MIT",
		"SPDX-License-Identifier MIT",
		"SPDX-License-Identifier: NoSuchLicense-1.0",
		"SPDX-License-Identifier: MIT AND",
		"SPDX-License-Identifier: (MIT OR ISC",
		"SPDX-License-Identifier: NONE",
		"SPDX-License-Identifier: NOASSERTION",
		"SPDX something else",
		strings.Repeat("s", 4096),
	}
	matcher := testSPDXMatcher(t)
	for _, input := range inputs {
		result, err := matcher.Match(context.Background(), []byte(input))
		if err != nil {
			t.Fatal(err)
		}
		for _, detection := range result.Detections {
			for _, match := range detection.Matches {
				if match.Method == SpdxID {
					t.Errorf("input %q produced SPDX-id match %#v", input, match)
				}
			}
		}
	}
}

func TestMatcherSPDXTagIdentifierListSkew(t *testing.T) {
	t.Parallel()

	const (
		identifier  = "Verbatim-man-pages"
		reportingID = "Linux-man-pages-copyleft"
	)
	if _, err := spdx.ParseStrict(identifier); err == nil {
		t.Fatalf("ParseStrict(%q) succeeded; test requires identifier-list skew", identifier)
	}

	matcher := testSPDXMatcher(t)
	input := "SPDX-License-Identifier: " + identifier
	match, ok := findSPDXMatch(t, matcher, input, reportingID)
	if !ok {
		return
	}
	if match.Start != 0 || match.End != len(input) {
		t.Errorf("span = [%d, %d), want [0, %d)", match.Start, match.End, len(input))
	}
}

func TestMatcherSPDXTagsMultiple(t *testing.T) {
	t.Parallel()

	input := "// SPDX-License-Identifier: MIT\n" +
		"// SPDX-License-Identifier: ISC\n"
	matcher := testSPDXMatcher(t)
	result, err := matcher.Match(context.Background(), []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool)
	for _, detection := range result.Detections {
		for _, match := range detection.Matches {
			if match.Method == SpdxID {
				got[detection.Expression] = true
			}
		}
	}
	if !got["MIT"] || !got["ISC"] || len(got) != 2 {
		t.Fatalf("expressions = %v, want MIT and ISC", got)
	}
}

func TestMatcherSPDXPreservesSeparateDeclarations(t *testing.T) {
	input := "SPDX-License-Identifier: GPL-3.0-or-later\n\n" +
		"SPDX-License-Identifier: GPL-3.0-only"
	matcher, err := New()
	if err != nil {
		t.Fatal(err)
	}
	result, err := matcher.Match(context.Background(), []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Detections) != 2 {
		t.Fatalf("detections = %+v", result.Detections)
	}
	for _, detection := range result.Detections {
		if detection.Expression != "GPL-3.0-only" && detection.Expression != "GPL-3.0-or-later" {
			t.Fatalf("unexpected expression: %s", detection.Expression)
		}
		if detection.Expression == "GPL-3.0-only" {
			for _, match := range detection.Matches {
				if match.Start < strings.LastIndex(input, "SPDX-License-Identifier:") {
					t.Fatalf("prefix match survived: %+v", match)
				}
			}
		}
	}
}

func TestMatcherSPDXTagsIdentification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  Identification
	}{
		{input: "SPDX-License-Identifier: MIT", want: Identified},
		{
			input: "SPDX-License-Identifier: MIT AND LicenseRef-something",
			want:  Partial,
		},
		{
			input: "SPDX-License-Identifier: LicenseRef-something",
			want:  NoAssertion,
		},
	}
	matcher := testSPDXMatcher(t)
	for _, test := range tests {
		result, err := matcher.Match(context.Background(), []byte(test.input))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Detections) == 0 {
			t.Fatalf("input %q produced no detections", test.input)
		}
		if got := result.Detections[0].Identification; got != test.want {
			t.Errorf("input %q identification = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestSPDXExpressionSpan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{input: " MIT\n", want: "MIT"},
		{input: " MIT\r\n", want: "MIT"},
		{input: " MIT */", want: "MIT"},
		{input: " MIT -->", want: "MIT"},
		{input: "\tMIT  ", want: "MIT"},
		{input: " Apache-2.0 OR MIT ", want: "Apache-2.0 OR MIT"},
		{input: "", want: ""},
	}
	for _, test := range tests {
		start, end := spdxExpressionSpan([]byte(test.input), 0)
		if got := test.input[start:end]; got != test.want {
			t.Errorf("span(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestSPDXExpressionSpanBounded(t *testing.T) {
	t.Parallel()

	input := make([]byte, maxSPDXExpressionBytes*2)
	for i := range input {
		input[i] = 'A'
	}
	_, end := spdxExpressionSpan(input, 0)
	if end != maxSPDXExpressionBytes {
		t.Fatalf("end = %d, want %d", end, maxSPDXExpressionBytes)
	}
}

func TestSPDXNormalizeExpression(t *testing.T) {
	t.Parallel()

	index := spdxIndex{
		keys: map[string]string{
			"mit":                     "mit",
			"apache-2.0":              "apache-2.0",
			"bsd-3-clause":            "bsd-new",
			"gpl-2.0":                 "gpl-2.0",
			"gpl-2.0+":                "gpl-2.0-plus",
			"classpath-exception-2.0": "classpath-exception-2.0",
			"verbatim-man-pages":      "verbatim-manual",
		},
		reportingIDs: map[string]string{
			"mit":                     "MIT",
			"apache-2.0":              "Apache-2.0",
			"bsd-new":                 "BSD-3-Clause",
			"gpl-2.0":                 "GPL-2.0-only",
			"gpl-2.0-plus":            "GPL-2.0-or-later",
			"classpath-exception-2.0": "Classpath-exception-2.0",
			"verbatim-manual":         "Linux-man-pages-copyleft",
			"unknown-spdx":            "LicenseRef-scancode-unknown-spdx",
		},
	}
	tests := []struct {
		input       string
		expression  string
		ids         []string
		scanCodeIDs []string
	}{
		{input: "MIT", expression: "MIT", ids: []string{"MIT"}, scanCodeIDs: []string{"mit"}},
		{
			input:       "MIT OR Apache-2.0",
			expression:  "MIT OR Apache-2.0",
			ids:         []string{"MIT", "Apache-2.0"},
			scanCodeIDs: []string{"mit", "apache-2.0"},
		},
		{
			input:       "( MIT  OR  BSD-3-Clause )",
			expression:  "MIT OR BSD-3-Clause",
			ids:         []string{"MIT", "BSD-3-Clause"},
			scanCodeIDs: []string{"mit", "bsd-new"},
		},
		{
			input:       "MIT AND MIT",
			expression:  "MIT AND MIT",
			ids:         []string{"MIT"},
			scanCodeIDs: []string{"mit"},
		},
		{
			input:       "GPL-2.0+ WITH Classpath-exception-2.0",
			expression:  "GPL-2.0-or-later WITH Classpath-exception-2.0",
			ids:         []string{"GPL-2.0-or-later", "Classpath-exception-2.0"},
			scanCodeIDs: []string{"gpl-2.0-plus", "classpath-exception-2.0"},
		},
		{
			input:       "MIT OR Apache-2.0 AND GPL-2.0+",
			expression:  "MIT OR (Apache-2.0 AND GPL-2.0-or-later)",
			ids:         []string{"MIT", "Apache-2.0", "GPL-2.0-or-later"},
			scanCodeIDs: []string{"mit", "apache-2.0", "gpl-2.0-plus"},
		},
		{
			input:       "LicenseRef-custom",
			expression:  "LicenseRef-scancode-unknown-spdx",
			ids:         []string{"LicenseRef-scancode-unknown-spdx"},
			scanCodeIDs: []string{"unknown-spdx"},
		},
		{
			input:       "DocumentRef-vendor:LicenseRef-custom",
			expression:  "LicenseRef-scancode-unknown-spdx",
			ids:         []string{"LicenseRef-scancode-unknown-spdx"},
			scanCodeIDs: []string{"unknown-spdx"},
		},
		{
			input:       "Verbatim-man-pages",
			expression:  "Linux-man-pages-copyleft",
			ids:         []string{"Linux-man-pages-copyleft"},
			scanCodeIDs: []string{"verbatim-manual"},
		},
		{input: "", expression: ""},
		{input: "unrecognised", expression: ""},
		{input: "MIT OR unrecognised", expression: ""},
		{input: "AND OR", expression: ""},
		{input: "MIT AND", expression: ""},
		{input: "(MIT OR Apache-2.0", expression: ""},
		{input: "MIT;", expression: ""},
		{input: "NONE", expression: ""},
		{input: "NOASSERTION", expression: ""},
	}
	for _, test := range tests {
		expression, ids, scanCodeIDs := index.normalizeExpression([]byte(test.input))
		if expression != test.expression {
			t.Errorf(
				"normalizeExpression(%q) = %q, want %q",
				test.input,
				expression,
				test.expression,
			)
		}
		if !slices.Equal(ids, test.ids) {
			t.Errorf(
				"normalizeExpression(%q) ids = %v, want %v",
				test.input,
				ids,
				test.ids,
			)
		}
		if !slices.Equal(scanCodeIDs, test.scanCodeIDs) {
			t.Errorf(
				"normalizeExpression(%q) ScanCode IDs = %v, want %v",
				test.input,
				scanCodeIDs,
				test.scanCodeIDs,
			)
		}
	}
}

func TestSPDXExpressionReporting(t *testing.T) {
	t.Parallel()

	index := spdxIndex{
		keys: map[string]string{
			"mit":                       "mit",
			"bsd-3-clause":              "bsd-new",
			"licenseref-scancode-other": "other",
		},
		reportingIDs: map[string]string{
			"mit":     "MIT",
			"bsd-new": "BSD-3-Clause",
		},
	}
	scanCode := "mit OR (bsd-new AND other)"
	expression, ids := index.reportExpression(scanCode)
	if expression != "MIT OR (BSD-3-Clause AND LicenseRef-scancode-other)" {
		t.Fatalf("reported expression = %q", expression)
	}
	wantIDs := []string{"MIT", "BSD-3-Clause", "LicenseRef-scancode-other"}
	if !slices.Equal(ids, wantIDs) {
		t.Fatalf("reported IDs = %v, want %v", ids, wantIDs)
	}
	got := rewriteExpressionIdentifiers(expression, func(identifier string) string {
		resolved, ok := index.resolve(identifier)
		if !ok {
			t.Fatalf("resolve(%q) failed", identifier)
		}
		return resolved
	})
	if got != scanCode {
		t.Fatalf("ScanCode expression = %q, want %q", got, scanCode)
	}
}

func TestBuildSPDXIndex(t *testing.T) {
	t.Parallel()

	index := buildSPDXIndex(corpus.Index{
		Rules: []corpus.Rule{
			{
				ID:         "mit_1.RULE",
				Expression: "mit",
				Flags:      corpus.FlagLicenseText,
			},
			{
				ID:         "compound.RULE",
				Expression: "gpl-2.0 WITH classpath-exception-2.0",
				Flags:      corpus.FlagLicenseNotice,
			},
			{
				ID:     "false-positive.RULE",
				Flags:  corpus.FlagFalsePositive,
				Tokens: []uint32{1},
			},
		},
		SPDXKeys: map[string]string{
			"bsd-3-clause": "bsd-new",
			"mit":          "mit",
		},
		ReportingIDs: map[string]string{
			"bsd-new": "BSD-3-Clause",
			"mit":     "MIT",
		},
	})

	tests := []struct {
		identifier string
		want       string
		ok         bool
	}{
		{identifier: "MIT", want: "mit", ok: true},
		{identifier: "BSD-3-Clause", want: "bsd-new", ok: true},
		{identifier: "gpl-2.0", want: "gpl-2.0", ok: true},
		{
			identifier: "classpath-exception-2.0",
			want:       "classpath-exception-2.0",
			ok:         true,
		},
		{identifier: "LicenseRef-scancode-mit", want: "mit", ok: true},
		{identifier: "LicenseRef-proprietary", want: "unknown-spdx", ok: true},
		{
			identifier: "DocumentRef-vendor:LicenseRef-custom",
			want:       "unknown-spdx",
			ok:         true,
		},
		{identifier: "unrecognised"},
	}
	for _, test := range tests {
		got, ok := index.resolve(test.identifier)
		if got != test.want || ok != test.ok {
			t.Errorf(
				"resolve(%q) = %q, %v; want %q, %v",
				test.identifier,
				got,
				ok,
				test.want,
				test.ok,
			)
		}
	}
}

func TestEmbeddedCorpusResolvesCommonSPDXIdentifiers(t *testing.T) {
	matcher, err := New()
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		"MIT":              "mit",
		"ISC":              "isc",
		"0BSD":             "bsd-zero",
		"BSD-2-Clause":     "bsd-simplified",
		"BSD-3-Clause":     "bsd-new",
		"Apache-2.0":       "apache-2.0",
		"GPL-2.0-only":     "gpl-2.0",
		"GPL-2.0-or-later": "gpl-2.0-plus",
		"GPL-2.0+":         "gpl-2.0-plus",
		"LGPL-2.1":         "lgpl-2.1",
		"MPL-2.0":          "mpl-2.0",
	}
	for spdx, want := range tests {
		got, ok := matcher.engine.spdx.resolve(spdx)
		if got != want || !ok {
			t.Errorf("resolve(%q) = %q, %v; want %q, true", spdx, got, ok, want)
		}
	}
}

func TestIndexSPDXAnchor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		from  int
		want  int
	}{
		{input: "SPDX", want: 0},
		{input: "spdx", want: 0},
		{input: "sPdX", want: 0},
		{input: " SPDX", want: 1},
		{input: "s SPDX", want: 2},
		{input: "s p SPDX", want: 4},
		{input: "no marker", want: -1},
		{input: "SPD", want: -1},
		{input: "SSPDX", want: 1},
		{input: "SPDX SPDX", from: 1, want: 5},
	}
	for _, test := range tests {
		if got := indexSPDXAnchor([]byte(test.input), test.from); got != test.want {
			t.Errorf("indexSPDXAnchor(%q, %d) = %d, want %d", test.input, test.from, got, test.want)
		}
	}
}

func testSPDXMatcher(t *testing.T) *Matcher {
	t.Helper()
	matcher, err := New(WithMatchedText())
	if err != nil {
		t.Fatal(err)
	}
	return matcher
}

func findSPDXMatch(t *testing.T, matcher *Matcher, input, expression string) (Match, bool) {
	t.Helper()
	result, err := matcher.Match(context.Background(), []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	for _, detection := range result.Detections {
		if detection.Expression != expression {
			continue
		}
		for _, match := range detection.Matches {
			if match.Method == SpdxID {
				return match, true
			}
		}
	}
	t.Errorf("no spdx-id match with expression %q in %#v", expression, result.Detections)
	return Match{}, false
}
