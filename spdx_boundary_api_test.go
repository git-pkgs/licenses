package licenses_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/git-pkgs/licenses"
)

func TestSPDXExpressionBoundary(t *testing.T) {
	t.Parallel()
	matcher, err := licenses.New()
	if err != nil {
		t.Fatal(err)
	}
	const limit = 1024
	for _, size := range []int{limit - 1, limit, limit + 1} {
		for _, ending := range []string{"", "\n", "\r\n", "*/", "-->"} {
			t.Run(fmt.Sprintf("%d/%q", size, ending), func(t *testing.T) {
				const suffix = "AND LicenseRef-Example"
				expression := "MIT" + strings.Repeat(" ", size-len("MIT")-len(suffix)) + suffix
				input := "SPDX-License-Identifier:" + expression + ending
				result, err := matcher.Match(context.Background(), []byte(input))
				if err != nil {
					t.Fatal(err)
				}
				if size > limit {
					requireSPDXCutOffClue(t, result, len(input)-len(ending))
					return
				}
				const want = "MIT AND LicenseRef-scancode-unknown-spdx"
				if len(result.SPDXDeclarations) != 1 || result.SPDXDeclarations[0].Expression != want ||
					result.SPDXDeclarations[0].End != len(input)-len(ending) {
					t.Fatalf("declarations = %+v", result.SPDXDeclarations)
				}
				if len(result.Detections) != 1 || result.Detections[0].Expression != want {
					t.Fatalf("detections = %+v", result.Detections)
				}
			})
		}
	}
}

func TestSPDXExpressionLimitDropsComponentMatches(t *testing.T) {
	t.Parallel()
	matcher, err := licenses.New()
	if err != nil {
		t.Fatal(err)
	}
	input := "SPDX-License-Identifier: MIT" + strings.Repeat(" ", 1020) + " AND LicenseRef-Example\n"
	result, err := matcher.Match(context.Background(), []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	requireSPDXCutOffClue(t, result, len(input)-1)
}

func TestSPDXExpressionLimitKeepsCoveringMatch(t *testing.T) {
	t.Parallel()
	matcher, err := licenses.New()
	if err != nil {
		t.Fatal(err)
	}
	input := "SPDX-License-Identifier: MIT" + strings.Repeat(" ", 1020) + " OR Apache-2.0\n"
	result, err := matcher.Match(context.Background(), []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	const want = "MIT OR Apache-2.0"
	if len(result.SPDXDeclarations) != 1 || result.SPDXDeclarations[0].Expression != want ||
		result.SPDXDeclarations[0].End != len(input)-1 {
		t.Fatalf("declarations = %+v", result.SPDXDeclarations)
	}
	if len(result.Detections) != 1 || result.Detections[0].Expression != want || len(result.Clues) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func requireSPDXCutOffClue(t *testing.T, result licenses.Result, end int) {
	t.Helper()
	if len(result.Detections) != 0 || len(result.SPDXDeclarations) != 0 {
		t.Fatalf("cut-off expression produced licensing: %+v", result)
	}
	if len(result.Clues) != 1 || result.Clues[0].RuleID != "spdx-license-identifier" ||
		result.Clues[0].Kind != licenses.KindClue || result.Clues[0].Start != 0 || result.Clues[0].End != end {
		t.Fatalf("clues = %+v", result.Clues)
	}
}

func TestScanRepositorySPDXExpressionLimit(t *testing.T) {
	t.Parallel()
	matcher, err := licenses.New()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cutOff := "// SPDX-License-Identifier: MIT" + strings.Repeat(" ", 1020) + " AND LicenseRef-Example\n"
	source := "// SPDX-License-Identifier: Apache-2.0\n" + cutOff
	// Source maps escape newlines, so a header tag runs past the limit.
	sourceMap := `{"sourcesContent":["// SPDX-License-Identifier: MIT\n` + strings.Repeat(`export const x = 1;\n`, 80) + `"]}`
	for name, content := range map[string]string{"main.go": source, "main.js.map": sourceMap} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	report, err := licenses.ScanRepository(context.Background(), matcher, root, licenses.DefaultScanOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("errors = %+v", report.Errors)
	}
	if len(report.Expressions) != 1 || report.Expressions[0].Expression != "Apache-2.0" {
		t.Fatalf("expressions = %+v", report.Expressions)
	}
	if report.Summary.FilesWithClues != 2 {
		t.Fatalf("summary = %+v", report.Summary)
	}
}
