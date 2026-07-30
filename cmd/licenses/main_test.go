package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"path/filepath"
	"strings"
	"testing"

	licenses "github.com/git-pkgs/licenses"
)

func TestRunVersion(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	exitCode, err := run(
		context.Background(),
		[]string{"-version"},
		&stdout,
		&stderr,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, want %d", exitCode, exitSuccess)
	}
	output := stdout.String()
	if !strings.HasPrefix(output, "licenses ") ||
		!strings.Contains(output, "ScanCode") ||
		!strings.Contains(output, "rules") {
		t.Fatalf("version output = %q", output)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunJSON(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode, err := run(
		context.Background(),
		[]string{"-json", "-matched-text", "../../LICENSE"},
		&stdout,
		&stderr,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != exitSuccess {
		t.Errorf("exit code = %d, want %d", exitCode, exitSuccess)
	}
	var report scanReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if report.Root != "../../LICENSE" {
		t.Errorf("root = %q, want ../../LICENSE", report.Root)
	}
	if report.Schema != reportSchemaVersion {
		t.Errorf("schema = %d, want %d", report.Schema, reportSchemaVersion)
	}
	if report.Summary.FilesScanned != 1 {
		t.Errorf("files scanned = %d, want 1", report.Summary.FilesScanned)
	}
	if report.Summary.FilesWithIdentifiedDetections != 1 ||
		report.Summary.FilesWithPartialDetections != 0 ||
		report.Summary.FilesWithNoAssertionDetections != 0 {
		t.Errorf("identification summary = %#v", report.Summary)
	}
	if len(report.Files) != 1 || !hasMITExpression(report.Files[0]) {
		t.Fatalf("files = %#v, want MIT detection", report.Files)
	}
	if report.Files[0].Detections[0].Identification != licenses.Identified {
		t.Errorf(
			"detection identification = %q, want %q",
			report.Files[0].Detections[0].Identification,
			licenses.Identified,
		)
	}
	if len(report.Expressions) != 1 ||
		report.Expressions[0].Identification != licenses.Identified {
		t.Errorf("expressions = %#v, want identified", report.Expressions)
	}
	if report.Files[0].Detections[0].Matches[0].Matched == "" {
		t.Error("matched text is empty")
	}
	match, ok := findRecordMatch(report.Files[0], "mit.LICENSE")
	if !ok {
		t.Fatal("mit.LICENSE match is absent")
	}
	if match.Kind != licenses.KindText {
		t.Errorf("match kind = %q, want %q", match.Kind, licenses.KindText)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunJSONIsDeterministicAcrossWorkerCounts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a", "LICENSE"), projectLicense(t))
	writeTestFile(t, filepath.Join(root, "b", "LICENSE"), projectLicense(t))
	writeTestFile(t, filepath.Join(root, "binary"), []byte("data\x00data"))

	var first bytes.Buffer
	var second bytes.Buffer
	firstExit, err := run(
		context.Background(),
		[]string{"-json", "-workers", "1", root},
		&first,
		&bytes.Buffer{},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondExit, err := run(
		context.Background(),
		[]string{"-json", "-workers", "8", root},
		&second,
		&bytes.Buffer{},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if firstExit != exitSuccess || secondExit != exitSuccess {
		t.Fatalf("exit codes = %d, %d; want success", firstExit, secondExit)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Errorf("JSON differs by worker count:\n%s\n%s", first.String(), second.String())
	}
}

func TestRunScope(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "vendor", "LICENSE"), projectLicense(t))

	var projectOutput bytes.Buffer
	projectExit, err := run(
		context.Background(),
		[]string{"-json", root},
		&projectOutput,
		&bytes.Buffer{},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if projectExit != exitNoDetections {
		t.Errorf("project exit = %d, want %d", projectExit, exitNoDetections)
	}
	var projectReport scanReport
	if err := json.Unmarshal(projectOutput.Bytes(), &projectReport); err != nil {
		t.Fatal(err)
	}
	if projectReport.Scope != "project" {
		t.Errorf("project scope = %q, want project", projectReport.Scope)
	}
	if !hasSkip(projectReport.Skipped, skipRecord{
		Path:   "vendor",
		Reason: "project-scope",
	}) {
		t.Errorf("project skipped = %#v, want vendor", projectReport.Skipped)
	}

	var allOutput bytes.Buffer
	allExit, err := run(
		context.Background(),
		[]string{"-json", "-scope", "all", "-max-files", "0", root},
		&allOutput,
		&bytes.Buffer{},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if allExit != exitSuccess {
		t.Errorf("all exit = %d, want %d", allExit, exitSuccess)
	}
	var allReport scanReport
	if err := json.Unmarshal(allOutput.Bytes(), &allReport); err != nil {
		t.Fatal(err)
	}
	if allReport.Scope != "all" {
		t.Errorf("all scope = %q, want all", allReport.Scope)
	}
	if len(allReport.Files) != 1 || !hasMITExpression(allReport.Files[0]) {
		t.Errorf("all files = %#v, want vendor MIT license", allReport.Files)
	}
}

func TestRunAllScopeRequiresMaxFiles(t *testing.T) {
	t.Parallel()

	exitCode, err := run(
		context.Background(),
		[]string{"-scope", "all"},
		&bytes.Buffer{},
		&bytes.Buffer{},
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "requires -max-files") {
		t.Fatalf("error = %v, want explicit max-files error", err)
	}
	if exitCode != exitFatal {
		t.Errorf("exit code = %d, want %d", exitCode, exitFatal)
	}
}

func TestRunNoDetections(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "plain.txt")
	writeTestFile(t, path, []byte("plain source text"))
	var stdout bytes.Buffer
	exitCode, err := run(
		context.Background(),
		[]string{"-json", path},
		&stdout,
		&bytes.Buffer{},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != exitNoDetections {
		t.Errorf("exit code = %d, want %d", exitCode, exitNoDetections)
	}
	for _, field := range []string{`"expressions": []`, `"files": []`, `"skipped": []`, `"errors": []`} {
		if !strings.Contains(stdout.String(), field) {
			t.Errorf("JSON does not contain %q:\n%s", field, stdout.String())
		}
	}
}

func TestRunHelpNamesPositionalArgument(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	exitCode, err := run(
		context.Background(),
		[]string{"-h"},
		&bytes.Buffer{},
		&stderr,
		false,
	)
	if err != flag.ErrHelp {
		t.Fatalf("error = %v, want flag.ErrHelp", err)
	}
	if exitCode != exitFatal {
		t.Errorf("exit code = %d, want %d before main handles help", exitCode, exitFatal)
	}
	for _, want := range []string{"Usage: licenses [flags] [path]", "default: current directory"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("help does not contain %q:\n%s", want, stderr.String())
		}
	}
}

func TestReportExitCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		report scanReport
		want   int
	}{
		{
			name: "detections",
			report: scanReport{Expressions: []expressionRecord{{
				Expression: "mit",
			}}},
			want: exitSuccess,
		},
		{
			name: "no detections",
			want: exitNoDetections,
		},
		{
			name: "scan errors",
			report: scanReport{Errors: []scanErrorRecord{{
				Path:  "LICENSE",
				Error: "unreadable",
			}}},
			want: exitScanErrors,
		},
		{
			name:   "file limit",
			report: scanReport{Summary: scanSummary{Truncated: true}},
			want:   exitScanErrors,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := reportExitCode(test.report); got != test.want {
				t.Errorf("exit code = %d, want %d", got, test.want)
			}
		})
	}
}

func TestRunHuman(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode, err := run(
		context.Background(),
		[]string{"-human", "../../LICENSE"},
		&stdout,
		&stderr,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != exitSuccess {
		t.Errorf("exit code = %d, want %d", exitCode, exitSuccess)
	}
	for _, want := range []string{
		"Detected expressions:",
		"mit:",
		"mit.LICENSE",
		"Scanned 1/1 files",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output does not contain %q:\n%s", want, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunRejectsConflictingFormats(t *testing.T) {
	t.Parallel()

	exitCode, err := run(
		context.Background(),
		[]string{"-json", "-human"},
		&bytes.Buffer{},
		&bytes.Buffer{},
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("error = %v, want conflicting format error", err)
	}
	if exitCode != exitFatal {
		t.Errorf("exit code = %d, want %d", exitCode, exitFatal)
	}
}

func TestParseSkippedDirectories(t *testing.T) {
	t.Parallel()

	directories := parseSkippedDirectories(" generated, docs ,,fixtures ")
	for _, name := range []string{"generated", "docs", "fixtures"} {
		if !directories[name] {
			t.Errorf("%q is not present in %#v", name, directories)
		}
	}
	if len(directories) != 3 {
		t.Errorf("directory count = %d, want 3", len(directories))
	}
}
