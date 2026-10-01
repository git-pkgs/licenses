package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/git-pkgs/licenses"
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
			stopwords := []byte("STOPWORDS = frozenset({\n    'quot',\n})\n")
			if err := os.WriteFile(filepath.Join(root, "src", "licensedcode", "stopwords.py"), stopwords, fileMode); err != nil {
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
			if err := run(root, versionPath, outputPath, "all"); err != nil {
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

func TestLoadStopwords(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "stopwords.py")
	data := []byte(`before = true
STOPWORDS = frozenset({
    # entities
    'quot',
    'amp', 'lt',  # multiple values
})
after = true
`)
	if err := os.WriteFile(path, data, fileMode); err != nil {
		t.Fatal(err)
	}
	got, err := loadStopwords(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"amp", "lt", "quot"}
	if !slices.Equal(got, want) {
		t.Fatalf("stopwords = %v, want %v", got, want)
	}
}

func TestLoadStopwordsRejectsMalformedEntry(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "stopwords.py")
	data := []byte("STOPWORDS = frozenset({\n    \"quot\",\n})\n")
	if err := os.WriteFile(path, data, fileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := loadStopwords(path); err == nil {
		t.Fatal("accepted a non-single-quoted stopword")
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

func TestParseRuleFlags(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input string
		want  uint16
	}{
		{"all", 0},
		{"text", corpus.FlagLicenseText},
		{" text, notice,text ", corpus.FlagLicenseText | corpus.FlagLicenseNotice},
		{"false-positive,required-phrase,continuous,deprecated", corpus.FlagFalsePositive | corpus.FlagRequiredPhrase | corpus.FlagContinuous | corpus.FlagDeprecated},
		{"tag,reference,intro,clue", corpus.FlagLicenseTag | corpus.FlagLicenseReference | corpus.FlagLicenseIntro | corpus.FlagLicenseClue},
	} {
		got, err := parseRuleFlags(test.input)
		if err != nil || got != test.want {
			t.Errorf("parseRuleFlags(%q) = %d, %v; want %d", test.input, got, err, test.want)
		}
	}
	for _, input := range []string{"", "texts", "text,", ",notice", "all,text"} {
		if _, err := parseRuleFlags(input); err == nil {
			t.Errorf("accepted invalid flags %q", input)
		}
	}
}

func TestBuildFilteredIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for name, data := range map[string]string{
		"stopwords.py":              "STOPWORDS = frozenset({\n    'quot',\n    'unused',\n})\n",
		"data/licenses/mit.LICENSE": "---\nkey: mit\nspdx_license_key: MIT\n---\nalpha quot beta\n",
		"data/rules/notice.RULE":    "---\nlicense_expression: mit\nis_license_notice: yes\nis_continuous: yes\n---\ngamma quot delta\n",
		"data/rules/reference.RULE": "---\nlicense_expression: mit\nis_license_reference: yes\n---\nexcluded vocabulary\n",
	} {
		path := filepath.Join(root, "src", "licensedcode", name)
		if err := os.MkdirAll(filepath.Dir(path), directoryMode); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), fileMode); err != nil {
			t.Fatal(err)
		}
	}
	version := sourceVersion{Version: "test", Commit: "test-commit"}
	for _, test := range []struct {
		name  string
		flags uint16
		count int
	}{
		{"all", 0, 3},
		{"text", corpus.FlagLicenseText, 1},
		{"text and notice", corpus.FlagLicenseText | corpus.FlagLicenseNotice, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			index, err := buildIndex(root, version, test.flags)
			if err != nil {
				t.Fatal(err)
			}
			if len(index.Rules) != test.count || index.Info.RuleCount != test.count {
				t.Fatalf("index info = %#v", index.Info)
			}
			if test.flags != 0 && slices.Contains(index.Vocabulary, "excluded") {
				t.Fatal("excluded vocabulary retained")
			}
			if test.flags == corpus.FlagLicenseText && slices.Contains(index.Vocabulary, "gamma") {
				t.Fatal("notice vocabulary retained in text-only corpus")
			}
			if len(index.StopwordIDs) != 2 {
				t.Fatalf("stopwords = %v", index.StopwordIDs)
			}
			for _, id := range index.StopwordIDs {
				if word := index.Vocabulary[id-1]; word != "quot" && word != "unused" {
					t.Fatalf("bad stopword remapping: %s", word)
				}
			}
			for i, rule := range index.Rules {
				state := uint32(0)
				for _, token := range rule.Tokens {
					state = index.Automaton.Next(state, token)
				}
				if !index.Automaton.HasOutput(state, uint32(i)) {
					t.Fatalf("missing automaton output for %s", rule.ID)
				}
				if rule.ID == "notice.RULE" && !slices.Equal(rule.StopwordAfter, []uint32{1}) {
					t.Fatalf("stopword positions = %v", rule.StopwordAfter)
				}
			}
			var first, second bytes.Buffer
			if err := corpus.Write(&first, index); err != nil {
				t.Fatal(err)
			}
			rebuilt, err := buildIndex(root, version, test.flags)
			if err != nil {
				t.Fatal(err)
			}
			if err := corpus.Write(&second, rebuilt); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first.Bytes(), second.Bytes()) {
				t.Fatal("filtered build is not deterministic")
			}
			matcher, err := licenses.NewFromReader(&first)
			if err != nil {
				t.Fatal(err)
			}
			result, err := matcher.Match(context.Background(), []byte("alpha quot beta"))
			if err != nil || len(result.Detections) != 1 || result.Detections[0].Expression != "MIT" {
				t.Fatalf("result = %#v, error = %v", result, err)
			}
			result, err = matcher.Match(context.Background(), []byte("gamma quot delta"))
			if err != nil {
				t.Fatal(err)
			}
			wantNotice := test.flags != corpus.FlagLicenseText
			if (len(result.Detections) != 0) != wantNotice {
				t.Fatalf("notice detections = %#v, want notice present: %t", result.Detections, wantNotice)
			}
		})
	}
	if _, err := buildIndex(root, version, corpus.FlagLicenseClue); err == nil {
		t.Fatal("accepted an empty selection")
	}
}
