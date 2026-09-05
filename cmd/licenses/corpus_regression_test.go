package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRunLargeFileWithDenseDeclarations(t *testing.T) {
	input := strings.Repeat("qzxw ", 300_000) + "MIT License\n" +
		strings.Repeat("SPDX-License-Identifier: GPL-3.0-or-later\n", 1000) +
		"SPDX-License-Identifier: GPL-3.0-only\n"
	path := filepath.Join(t.TempDir(), "LICENSE")
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code, err := run(context.Background(), []string{"-json", "-max-file-size", "0", path}, &stdout, &stderr, false)
	if err != nil || code != exitSuccess {
		t.Fatalf("run = %d, %v: %s", code, err, stderr.String())
	}
	var report scanReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Summary.BytesScanned != int64(len(input)) || report.Summary.FilesScanned != 1 || report.Summary.ErrorCount != 0 {
		t.Fatalf("summary = %+v", report.Summary)
	}
	want := map[string]int{"MIT": 1, "GPL-3.0-or-later": 1000, "GPL-3.0-only": 1}
	if len(report.Expressions) != len(want) {
		t.Fatalf("expressions = %+v", report.Expressions)
	}
	for _, expression := range report.Expressions {
		if expression.Matches != want[expression.Expression] {
			t.Fatalf("expression = %+v", expression)
		}
	}
}

func TestRunCorpusRegressions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		file string
		want []string
	}{
		{"spdx-alternatives.txt", []string{"AGPL-3.0-or-later OR GPL-2.0-only"}},
		{"spdx-custom-alternative.txt", []string{"GPL-2.0-only OR GPL-3.0-only OR LicenseRef-scancode-kde-accepted-gpl"}},
		{"spdx-later-version.txt", []string{"GPL-3.0-or-later"}},
		{"bsd-three-conditions.txt", nil},
		{"apache-attribution.txt", nil},
	}
	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code, err := run(context.Background(), []string{"-json", filepath.Join("../../testdata/software-heritage", test.file)}, &stdout, &stderr, false)
			if err != nil {
				t.Fatal(err)
			}
			wantCode := exitSuccess
			if len(test.want) == 0 {
				wantCode = exitNoDetections
			}
			if code != wantCode {
				t.Errorf("exit = %d, want %d", code, wantCode)
			}
			var report scanReport
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, expression := range report.Expressions {
				got = append(got, expression.Expression)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("expressions = %v, want %v", got, test.want)
			}
			if report.Summary.FilesScanned != 1 || report.Summary.ErrorCount != 0 {
				t.Fatalf("summary = %+v", report.Summary)
			}
			if len(test.want) == 0 && report.Summary.FilesWithClues != 1 {
				t.Fatalf("missing clue evidence: %+v", report.Summary)
			}
		})
	}
}
