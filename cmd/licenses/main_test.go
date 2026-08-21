package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"path/filepath"
	"slices"
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
	if strings.Contains(stdout.String(), `"text":`) {
		t.Errorf("default JSON contains legal file text:\n%s", stdout.String())
	}
	if report.Root != "../../LICENSE" {
		t.Errorf("root = %q, want ../../LICENSE", report.Root)
	}
	if report.Schema != reportSchemaVersion {
		t.Errorf("schema = %d, want %d", report.Schema, reportSchemaVersion)
	}
	if report.Scanner != (scannerRecord{Name: scannerName, Version: version}) {
		t.Errorf("scanner = %#v, want CLI name and version", report.Scanner)
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
	digest := sha256.Sum256(projectLicense(t))
	if report.Files[0].SHA256 != hex.EncodeToString(digest[:]) {
		t.Errorf("sha256 = %q, want original LICENSE hash", report.Files[0].SHA256)
	}
	if !slices.Equal(report.Files[0].Roles, []string{"license"}) {
		t.Errorf("roles = %#v, want license", report.Files[0].Roles)
	}
	if report.Files[0].LicenseTextCoverage <= 0 ||
		report.Files[0].LicenseTextCoverage > 100 {
		t.Errorf(
			"license text coverage = %v, want within (0, 100]",
			report.Files[0].LicenseTextCoverage,
		)
	}
	if !strings.Contains(stdout.String(), `"license_text_coverage":`) {
		t.Errorf("JSON does not contain license_text_coverage:\n%s", stdout.String())
	}
	if report.Files[0].Detections[0].Identification != licenses.Identified {
		t.Errorf(
			"detection identification = %q, want %q",
			report.Files[0].Detections[0].Identification,
			licenses.Identified,
		)
	}
	if len(report.Expressions) != 1 ||
		report.Expressions[0].Identification != licenses.Identified ||
		!report.Expressions[0].Root {
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

func TestRunJSONReportsEmptyRolesAndNonRootExpression(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "source.txt")
	writeTestFile(t, path, projectLicense(t))
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
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, want %d", exitCode, exitSuccess)
	}
	var report scanReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Files) != 1 || report.Files[0].Roles == nil || len(report.Files[0].Roles) != 0 {
		t.Fatalf("files = %#v, want one file with empty roles", report.Files)
	}
	if len(report.Expressions) != 1 || report.Expressions[0].Root {
		t.Errorf("expressions = %#v, want one non-root expression", report.Expressions)
	}
	if !strings.Contains(stdout.String(), `"roles": []`) ||
		!strings.Contains(stdout.String(), `"root": false`) {
		t.Errorf("JSON does not preserve empty roles and false root:\n%s", stdout.String())
	}
}

func TestRunJSONIsDeterministicAcrossWorkerCounts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a", "LICENSE"), projectLicense(t))
	writeTestFile(t, filepath.Join(root, "b", "LICENSE"), projectLicense(t))
	writeTestFile(t, filepath.Join(root, "licenses", "NOTICE"), projectLicense(t))
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
	var report scanReport
	if err := json.Unmarshal(first.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	foundNotice := false
	for _, file := range report.Files {
		if file.Path == "licenses/NOTICE" &&
			slices.Equal(file.Roles, []string{"license", "notice"}) {
			foundNotice = true
		}
	}
	if !foundNotice {
		t.Errorf("files = %#v, want NOTICE with license, notice roles", report.Files)
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
	for _, field := range []string{`"declared": []`, `"expressions": []`, `"files": []`, `"skipped": []`, `"errors": []`} {
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
				Expression: "MIT",
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
		"MIT:",
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
