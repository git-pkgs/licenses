package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/git-pkgs/licenses/internal/corpus"
)

func TestReadSourceVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		commit string
	}{
		{name: "SHA-1 lowercase", commit: "0123456789abcdef0123456789abcdef01234567"},
		{name: "SHA-1 uppercase", commit: "0123456789ABCDEF0123456789ABCDEF01234567"},
		{name: "SHA-256 lowercase", commit: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{name: "SHA-256 uppercase", commit: "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "CORPUS_VERSION")
			data := []byte("version=1.2.3\ncommit=" + test.commit + "\n")
			if err := os.WriteFile(path, data, fileMode); err != nil {
				t.Fatal(err)
			}
			got, err := readSourceVersion(path)
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != "1.2.3" || got.Commit != strings.ToLower(test.commit) {
				t.Fatalf("version = %#v", got)
			}
		})
	}
}

func TestRunNormalizesUppercaseCommit(t *testing.T) {
	t.Parallel()

	for _, objectFormat := range []string{"sha1", "sha256"} {
		t.Run(objectFormat, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "scancode")
			dataRoot := filepath.Join(root, "src", "licensedcode", "data")
			if err := os.MkdirAll(filepath.Join(dataRoot, "licenses"), directoryMode); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(dataRoot, "rules"), directoryMode); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "README"), []byte("fixture\n"), fileMode); err != nil {
				t.Fatal(err)
			}

			runGit(t, root, "init", "--object-format="+objectFormat)
			runGit(t, root, "add", ".")
			runGit(
				t,
				root,
				"-c", "user.name=Corpus Test",
				"-c", "user.email=corpus@example.com",
				"-c", "commit.gpgsign=false",
				"commit", "-m", "fixture",
			)
			commit := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
			versionPath := filepath.Join(base, "CORPUS_VERSION")
			versionData := []byte("version=1.2.3\ncommit=" + strings.ToUpper(commit) + "\n")
			if err := os.WriteFile(versionPath, versionData, fileMode); err != nil {
				t.Fatal(err)
			}

			outputPath := filepath.Join(base, "corpus.bin.gz")
			if err := run(root, versionPath, outputPath); err != nil {
				t.Fatal(err)
			}
			output, err := os.Open(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			index, readErr := corpus.Read(output)
			closeErr := output.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			if index.Info.SourceCommit != commit {
				t.Fatalf("source commit = %q, want %q", index.Info.SourceCommit, commit)
			}
		})
	}
}

func TestReadSourceVersionRejectsInvalidCommit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		commit  string
		wantErr string
	}{
		{name: "short", commit: strings.Repeat("a", 39), wantErr: "commit must be a full 40- or 64-character object ID"},
		{name: "between hashes", commit: strings.Repeat("a", 41), wantErr: "commit must be a full 40- or 64-character object ID"},
		{name: "long", commit: strings.Repeat("a", 65), wantErr: "commit must be a full 40- or 64-character object ID"},
		{name: "non-hex", commit: strings.Repeat("a", 39) + "g", wantErr: "invalid commit"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "CORPUS_VERSION")
			data := []byte("version=1.2.3\ncommit=" + test.commit + "\n")
			if err := os.WriteFile(path, data, fileMode); err != nil {
				t.Fatal(err)
			}
			_, err := readSourceVersion(path)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want error containing %q", err, test.wantErr)
			}
		})
	}
}

func TestLoadRule(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sample.RULE")
	data := []byte(`---
license_expression: AGPL-3.0
is_license_notice: yes
is_false_positive: yes
relevance: 85
---

This package is licensed under the AGPL.
`)
	if err := os.WriteFile(path, data, fileMode); err != nil {
		t.Fatal(err)
	}
	got, err := loadRule(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Expression != "AGPL-3.0" {
		t.Fatalf("expression = %q", got.Expression)
	}
	if got.Relevance != 85 {
		t.Fatalf("relevance = %d", got.Relevance)
	}
	wantFlags := corpus.FlagLicenseNotice | corpus.FlagFalsePositive
	if got.Flags != wantFlags {
		t.Fatalf("flags = %d, want %d", got.Flags, wantFlags)
	}
	if !bytes.Equal(got.Text, []byte("\nThis package is licensed under the AGPL.\n")) {
		t.Fatalf("text = %q", got.Text)
	}
}

func TestLoadLicenseTextUsesKeyAsExpression(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "apache-2.0.LICENSE")
	data := []byte("---\nkey: apache-2.0\n---\nApache License\n")
	if err := os.WriteFile(path, data, fileMode); err != nil {
		t.Fatal(err)
	}
	got, err := loadRule(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Expression != "apache-2.0" {
		t.Fatalf("expression = %q", got.Expression)
	}
	if got.Flags&corpus.FlagLicenseText == 0 {
		t.Fatal("license text flag is not set")
	}
}

func TestLoadFalsePositiveWithoutExpression(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "false-positive.RULE")
	data := []byte("\n---\nis_false_positive: yes\n---\nnot a license\n")
	if err := os.WriteFile(path, data, fileMode); err != nil {
		t.Fatal(err)
	}
	got, err := loadRule(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Expression != "" {
		t.Fatalf("expression = %q", got.Expression)
	}
	if got.Flags&corpus.FlagFalsePositive == 0 {
		t.Fatal("false-positive flag is not set")
	}
}

func TestLoadSPDXMappings(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	licenses := map[string]string{
		"bsd-new.LICENSE": "---\n" +
			"key: bsd-new\n" +
			"spdx_license_key: BSD-3-Clause\n" +
			"other_spdx_license_keys:\n" +
			"    - LicenseRef-scancode-bsd-new\n" +
			"---\nBody\n",
		"mit.LICENSE": "---\nkey: mit\nspdx_license_key: MIT\n---\nBody\n",
		"no-spdx.LICENSE": "---\n" +
			"key: no-spdx\n" +
			"other_spdx_license_keys:\n" +
			"    - MIT\n" +
			"---\nBody\n",
	}
	for name, data := range licenses {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(data), fileMode); err != nil {
			t.Fatal(err)
		}
	}

	gotKeys, gotReportingIDs, err := loadSPDXMappings(directory)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys := map[string]string{
		"mit":                         "mit",
		"bsd-new":                     "bsd-new",
		"bsd-3-clause":                "bsd-new",
		"licenseref-scancode-bsd-new": "bsd-new",
		"no-spdx":                     "no-spdx",
		"licenseref-scancode-no-spdx": "no-spdx",
	}
	if len(gotKeys) != len(wantKeys) {
		t.Fatalf("keys = %#v, want %#v", gotKeys, wantKeys)
	}
	for key, value := range wantKeys {
		if gotKeys[key] != value {
			t.Errorf("key %q = %q, want %q", key, gotKeys[key], value)
		}
	}
	wantReportingIDs := map[string]string{
		"bsd-new": "BSD-3-Clause",
		"mit":     "MIT",
		"no-spdx": "LicenseRef-scancode-no-spdx",
	}
	if len(gotReportingIDs) != len(wantReportingIDs) {
		t.Fatalf("reporting IDs = %#v, want %#v", gotReportingIDs, wantReportingIDs)
	}
	for key, value := range wantReportingIDs {
		if gotReportingIDs[key] != value {
			t.Errorf("reporting ID %q = %q, want %q", key, gotReportingIDs[key], value)
		}
	}
}

func TestAddSPDXKeyPrecedence(t *testing.T) {
	t.Parallel()

	keys := make(map[string]string)
	addSPDXKey(keys, "MIT", "mit")
	addSPDXKey(keys, "mit", "other")
	addSPDXKey(keys, "", "ignored")
	if keys["mit"] != "mit" {
		t.Fatalf("mit = %q, want %q", keys["mit"], "mit")
	}
	if _, ok := keys[""]; ok {
		t.Fatal("empty key was added")
	}
}

func TestSplitFrontmatterRejectsIncompleteInput(t *testing.T) {
	t.Parallel()

	if _, _, err := splitFrontmatter([]byte("license_expression: mit\n")); err == nil {
		t.Fatal("accepted input without delimiters")
	}
	if _, _, err := splitFrontmatter([]byte("---\nlicense_expression: mit\n")); err == nil {
		t.Fatal("accepted input without closing delimiter")
	}
}

func TestVerifyCheckoutRejectsUntrackedCorpusData(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dataRoot := filepath.Join(root, "src", "licensedcode", "data")
	if err := os.MkdirAll(dataRoot, directoryMode); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(dataRoot, "tracked.RULE")
	if err := os.WriteFile(tracked, []byte("tracked"), fileMode); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init")
	runGit(t, root, "add", ".")
	runGit(
		t,
		root,
		"-c", "user.name=Corpus Test",
		"-c", "user.email=corpus@example.com",
		"-c", "commit.gpgsign=false",
		"commit", "-m", "fixture",
	)
	commit := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))

	if err := verifyCheckout(root, commit); err != nil {
		t.Fatalf("clean checkout: %v", err)
	}
	untracked := filepath.Join(dataRoot, "untracked.RULE")
	if err := os.WriteFile(untracked, []byte("untracked"), fileMode); err != nil {
		t.Fatal(err)
	}
	if err := verifyCheckout(root, commit); err == nil {
		t.Fatal("accepted untracked corpus data")
	}
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
