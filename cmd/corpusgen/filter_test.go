package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/git-pkgs/licenses"
	"github.com/git-pkgs/licenses/internal/corpus"
)

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
