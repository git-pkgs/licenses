package licenses_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/git-pkgs/licenses"
	"github.com/git-pkgs/licenses/internal/aho"
	"github.com/git-pkgs/licenses/internal/corpus"
)

func readerCorpus(t *testing.T, expression string) []byte {
	t.Helper()
	automaton, err := aho.Build([]aho.Pattern{{Tokens: []uint32{1, 2}, Value: 0}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	index := corpus.Index{
		Info:        corpus.Info{Version: "test", SourceCommit: "test-commit", RuleCount: 1},
		Vocabulary:  []string{"alpha", "beta", "quot"},
		StopwordIDs: []uint32{3},
		Rules: []corpus.Rule{{ID: "test.LICENSE", Expression: expression,
			Tokens: []uint32{1, 2}, Flags: corpus.FlagLicenseText, Relevance: 100}},
		Automaton:    automaton,
		SPDXKeys:     map[string]string{"mit": "mit", "isc": "isc"},
		ReportingIDs: map[string]string{"mit": "MIT", "isc": "ISC"},
	}
	var data bytes.Buffer
	if err := corpus.Write(&data, index); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestNewFromReader(t *testing.T) {
	t.Parallel()
	first, err := licenses.NewFromReader(bytes.NewReader(readerCorpus(t, "mit")), licenses.WithMatchedText())
	if err != nil {
		t.Fatal(err)
	}
	second, err := licenses.NewFromReader(bytes.NewReader(readerCorpus(t, "isc")))
	if err != nil {
		t.Fatal(err)
	}
	if got := first.Corpus(); got.Version != "test" || got.SourceCommit != "test-commit" || got.RuleCount != 1 {
		t.Fatalf("corpus = %#v", got)
	}
	for _, test := range []struct {
		input  string
		method licenses.Method
	}{
		{"alpha quot beta", licenses.Hash},
		{"beta alpha beta alpha", licenses.Exact},
	} {
		result, err := first.Match(context.Background(), []byte(test.input))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Detections) != 1 || result.Detections[0].Expression != "MIT" {
			t.Fatalf("%q: detections = %#v", test.input, result.Detections)
		}
		match := result.Detections[0].Matches[0]
		if match.Method != test.method || string(match.Matched) != test.input[match.Start:match.End] {
			t.Fatalf("match = %#v", match)
		}
	}
	result, err := second.Match(context.Background(), []byte("alpha beta"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Detections) != 1 || result.Detections[0].Expression != "ISC" ||
		result.Detections[0].Matches[0].Matched != nil {
		t.Fatalf("independent matcher result = %#v", result)
	}
	result, err = first.Match(context.Background(), []byte("SPDX-License-Identifier: ISC"))
	if err != nil || len(result.Detections) != 1 || result.Detections[0].Expression != "ISC" {
		t.Fatalf("SPDX result = %#v, error = %v", result, err)
	}
}

func TestNewFromReaderRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	valid := readerCorpus(t, "mit")
	corrupt := bytes.Clone(valid)
	corrupt[len(corrupt)-8] ^= 1 // gzip checksum
	for name, reader := range map[string]io.Reader{
		"nil":       nil,
		"empty":     bytes.NewReader(nil),
		"invalid":   bytes.NewBufferString("not a corpus"),
		"truncated": bytes.NewReader(valid[:len(valid)/2]),
		"checksum":  bytes.NewReader(corrupt),
	} {
		t.Run(name, func(t *testing.T) {
			if matcher, err := licenses.NewFromReader(reader); err == nil || matcher != nil {
				t.Fatalf("matcher = %v, error = %v", matcher, err)
			}
		})
	}
	if _, err := licenses.NewFromReader(bytes.NewReader(valid), nil); err == nil {
		t.Fatal("accepted nil option")
	}
	want := errors.New("read failure")
	if _, err := licenses.NewFromReader(failingCorpusReader{want}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestNewFromReaderLeavesReaderOpen(t *testing.T) {
	t.Parallel()
	file, err := os.CreateTemp(t.TempDir(), "corpus-*.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(readerCorpus(t, "mit")); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	matcher, err := licenses.NewFromReader(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Stat(); err != nil {
		t.Fatalf("constructor closed the file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := matcher.Match(context.Background(), []byte("alpha beta"))
	if err != nil || len(result.Detections) != 1 {
		t.Fatalf("match after closing reader = %#v, error = %v", result, err)
	}
}

type failingCorpusReader struct{ err error }

func (r failingCorpusReader) Read([]byte) (int, error) { return 0, r.err }
