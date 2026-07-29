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

	path := filepath.Join(t.TempDir(), "CORPUS_VERSION")
	data := []byte("version=1.2.3\ncommit=0123456789abcdef0123456789abcdef01234567\n")
	if err := os.WriteFile(path, data, fileMode); err != nil {
		t.Fatal(err)
	}
	got, err := readSourceVersion(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "1.2.3" || got.Commit != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("version = %#v", got)
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
