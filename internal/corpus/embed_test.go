package corpus

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/git-pkgs/licenses/internal/aho"
)

func TestEmbeddedCorpus(t *testing.T) {
	index, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if index.Info.Version != "33.0.0rc1" {
		t.Fatalf("version = %q", index.Info.Version)
	}
	if index.Info.SourceCommit != "6ba59089789600479cda46c84c6d436774179092" {
		t.Fatalf("source commit = %q", index.Info.SourceCommit)
	}
	if index.Info.RuleCount != 39_215 {
		t.Fatalf("rule count = %d, want 39215", index.Info.RuleCount)
	}
	if len(index.Rules) != index.Info.RuleCount {
		t.Fatalf("decoded %d rules, metadata reports %d", len(index.Rules), index.Info.RuleCount)
	}
	var tokenCount int
	for _, rule := range index.Rules {
		tokenCount += len(rule.Tokens)
	}
	var zeroFailures, noOutputLinks, noTerminals int
	for node := range index.Automaton.NodeCount() {
		if index.Automaton.Failures[node] == 0 {
			zeroFailures++
		}
		if index.Automaton.OutputLinks[node] == ^uint32(0) {
			noOutputLinks++
		}
		if index.Automaton.TerminalHeads[node] == ^uint32(0) {
			noTerminals++
		}
	}
	t.Logf(
		"%d words, %d tokens, %d automaton nodes, %d automaton edges, %d zero failures, %d empty output links, %d nonterminal nodes",
		len(index.Vocabulary),
		tokenCount,
		index.Automaton.NodeCount(),
		index.Automaton.EdgeCount(),
		zeroFailures,
		noOutputLinks,
		noTerminals,
	)
}

func TestEmbeddedCorpusSize(t *testing.T) {
	const maximumSize = 16 << 20
	if EmbeddedSize() > maximumSize {
		t.Fatalf("embedded corpus is %d bytes, budget is %d", EmbeddedSize(), maximumSize)
	}
}

func TestEmbeddedCorpusHash(t *testing.T) {
	wantBytes, err := os.ReadFile("../../CORPUS_SHA256")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(string(wantBytes))
	got := fmt.Sprintf("%x", sha256.Sum256(embedded))
	if got != want {
		t.Fatalf("embedded corpus hash = %s, want %s", got, want)
	}
}

func TestEmbeddedAutomatonMatchesEveryRule(t *testing.T) {
	index, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for ruleIndex, rule := range index.Rules {
		if len(rule.Tokens) == 0 {
			continue
		}
		state := uint32(0)
		for _, token := range rule.Tokens {
			state = index.Automaton.Next(state, token)
		}
		outputs := index.Automaton.AppendOutputs(nil, state)
		if !slices.Contains(outputs, uint32(ruleIndex)) {
			t.Fatalf("%s is absent from its terminal output", rule.ID)
		}
	}
}

func BenchmarkEmbeddedCorpusLoad(b *testing.B) {
	for b.Loop() {
		index, err := Load()
		if err != nil {
			b.Fatal(err)
		}
		if len(index.Rules) != 39_215 {
			b.Fatalf("decoded %d rules", len(index.Rules))
		}
	}
}

func BenchmarkEmbeddedCorpusDecompress(b *testing.B) {
	for b.Loop() {
		reader, err := gzip.NewReader(bytes.NewReader(embedded))
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, reader); err != nil {
			b.Fatal(err)
		}
		if err := reader.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEmbeddedCorpusBuildFailureLinks(b *testing.B) {
	index, err := Load()
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		failures, outputLinks, err := aho.BuildFailureLinks(
			index.Automaton.EdgeStarts,
			index.Automaton.EdgeTokens,
			index.Automaton.TerminalHeads,
		)
		if err != nil {
			b.Fatal(err)
		}
		if len(failures) != index.Automaton.NodeCount() || len(outputLinks) != index.Automaton.NodeCount() {
			b.Fatal("incorrect link counts")
		}
	}
}

func BenchmarkEmbeddedCorpusValidate(b *testing.B) {
	index, err := Load()
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		if err := index.Automaton.Validate(len(index.Rules)); err != nil {
			b.Fatal(err)
		}
	}
}
