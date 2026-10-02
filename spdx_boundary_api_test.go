package licenses_test

import (
	"context"
	"errors"
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
				if size > limit {
					requireSPDXLimitError(t, result, err)
					return
				}
				if err != nil {
					t.Fatal(err)
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

func TestSPDXExpressionLimitDiscardsComponentMatches(t *testing.T) {
	t.Parallel()
	matcher, err := licenses.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{" AND LicenseRef-Example", " OR Apache-2.0"} {
		input := "SPDX-License-Identifier: MIT" + strings.Repeat(" ", 1020) + suffix + "\n"
		result, err := matcher.Match(context.Background(), []byte(input))
		requireSPDXLimitError(t, result, err)
	}
}

func requireSPDXLimitError(t *testing.T, result licenses.Result, err error) {
	t.Helper()
	if !errors.Is(err, licenses.ErrSPDXExpressionTooLarge) {
		t.Fatalf("expected expression limit error, got %v; result=%+v", err, result)
	}
	if len(result.Detections) != 0 || len(result.SPDXDeclarations) != 0 || len(result.Clues) != 0 {
		t.Fatalf("partial result escaped: %+v", result)
	}
}

func TestScanRepositorySPDXExpressionLimit(t *testing.T) {
	t.Parallel()
	matcher, err := licenses.New()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	input := "// SPDX-License-Identifier: MIT" + strings.Repeat(" ", 1020) + " AND LicenseRef-Example\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := licenses.ScanRepository(context.Background(), matcher, root, licenses.DefaultScanOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Errors) != 1 || report.Errors[0].Path != "main.go" || report.Summary.ErrorCount != 1 {
		t.Fatalf("scan did not report the incomplete analysis: %+v", report)
	}
	if len(report.Expressions) != 0 || len(report.Files) != 0 {
		t.Fatalf("scan reported partial licensing: %+v", report)
	}
}
