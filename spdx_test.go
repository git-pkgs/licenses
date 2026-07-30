package licenses

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/git-pkgs/licenses/internal/corpus"
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
			expression: "mit",
			start:      0,
			end:        28,
		},
		{
			name:       "aliased id",
			input:      "SPDX-License-Identifier: BSD-3-Clause",
			expression: "bsd-new",
			start:      0,
			end:        37,
		},
		{
			name:       "line comment",
			input:      "// Copyright.\n// SPDX-License-Identifier: MIT\npackage p\n",
			expression: "mit",
			start:      17,
			end:        45,
		},
		{
			name:       "block comment",
			input:      "/* SPDX-License-Identifier: MIT */",
			expression: "mit",
			start:      3,
			end:        31,
		},
		{
			name:       "html comment",
			input:      "<!-- SPDX-License-Identifier: MIT -->",
			expression: "mit",
			start:      5,
			end:        33,
		},
		{
			name:       "crlf",
			input:      "SPDX-License-Identifier: MIT\r\n",
			expression: "mit",
			start:      0,
			end:        28,
		},
		{
			name:       "lowercase tag",
			input:      "spdx-license-identifier: mit",
			expression: "mit",
			start:      0,
			end:        28,
		},
		{
			name:       "typo tag",
			input:      "SPDX-License-Identifer: MIT",
			expression: "mit",
			start:      0,
			end:        27,
		},
		{
			name:       "compound",
			input:      "SPDX-License-Identifier: MIT OR Apache-2.0",
			expression: "mit OR apache-2.0",
			start:      0,
			end:        42,
		},
		{
			name:       "parenthesised",
			input:      "SPDX-License-Identifier: (MIT OR BSD-3-Clause)",
			expression: "(mit OR bsd-new)",
			start:      0,
			end:        46,
		},
		{
			name:       "with exception",
			input:      "SPDX-License-Identifier: GPL-2.0-only WITH Classpath-exception-2.0",
			expression: "gpl-2.0 WITH classpath-exception-2.0",
			start:      0,
			end:        66,
		},
		{
			name:       "scancode license ref",
			input:      "SPDX-License-Identifier: LicenseRef-scancode-bsd-new",
			expression: "bsd-new",
			start:      0,
			end:        52,
		},
		{
			name:       "unknown license ref",
			input:      "SPDX-License-Identifier: LicenseRef-Proprietary",
			expression: "unknown-spdx",
			start:      0,
			end:        47,
		},
		{
			name:       "unknown id",
			input:      "SPDX-License-Identifier: NoSuchLicense-1.0",
			expression: "unknown-spdx",
			start:      0,
			end:        42,
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

func TestMatcherSPDXTagsNoMatch(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"",
		"SPDX-License-Identifier:",
		"SPDX-License-Identifier: ",
		"SPDX-License-Identifier: \n",
		"SPDX License Identifier: MIT",
		"SPDX-License-Identifier MIT",
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

func TestMatcherSPDXTagsMultiple(t *testing.T) {
	t.Parallel()

	input := "// SPDX-License-Identifier: MIT\n" +
		"// SPDX-License-Identifier: BSD-3-Clause\n"
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
	if !got["mit"] || !got["bsd-new"] || len(got) != 2 {
		t.Fatalf("expressions = %v, want mit and bsd-new", got)
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

	index := spdxIndex{keys: map[string]string{
		"mit":          "mit",
		"apache-2.0":   "apache-2.0",
		"bsd-3-clause": "bsd-new",
	}}
	tests := []struct {
		input      string
		expression string
		ids        []string
	}{
		{input: "MIT", expression: "mit", ids: []string{"mit"}},
		{
			input:      "MIT OR Apache-2.0",
			expression: "mit OR apache-2.0",
			ids:        []string{"mit", "apache-2.0"},
		},
		{
			input:      "( MIT  OR  BSD-3-Clause )",
			expression: "(mit OR bsd-new)",
			ids:        []string{"mit", "bsd-new"},
		},
		{
			input:      "MIT AND MIT",
			expression: "mit AND mit",
			ids:        []string{"mit"},
		},
		{
			input:      "unrecognised",
			expression: "unknown-spdx",
			ids:        []string{"unknown-spdx"},
		},
		{input: "", expression: ""},
		{input: "AND OR", expression: ""},
		{input: "MIT;", expression: ""},
	}
	for _, test := range tests {
		expression, ids := index.normalizeExpression([]byte(test.input))
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
	})

	tests := []struct {
		identifier string
		want       string
	}{
		{identifier: "MIT", want: "mit"},
		{identifier: "BSD-3-Clause", want: "bsd-new"},
		{identifier: "gpl-2.0", want: "gpl-2.0"},
		{identifier: "classpath-exception-2.0", want: "classpath-exception-2.0"},
		{identifier: "LicenseRef-scancode-mit", want: "mit"},
		{identifier: "LicenseRef-proprietary", want: "unknown-spdx"},
		{identifier: "unrecognised", want: "unknown-spdx"},
	}
	for _, test := range tests {
		if got := index.resolve(test.identifier); got != test.want {
			t.Errorf("resolve(%q) = %q, want %q", test.identifier, got, test.want)
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
		"LGPL-2.1":         "lgpl-2.1",
		"MPL-2.0":          "mpl-2.0",
	}
	for spdx, want := range tests {
		if got := matcher.engine.spdx.resolve(spdx); got != want {
			t.Errorf("resolve(%q) = %q, want %q", spdx, got, want)
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
