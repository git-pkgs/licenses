package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/git-pkgs/licenses/internal/aho"
	"github.com/git-pkgs/licenses/internal/corpus"
)

const (
	previousCommit = "0123456789abcdef0123456789abcdef01234567"
	currentCommit  = "89abcdef0123456789abcdef0123456789abcdef"
)

func TestLoadBenchmarksUsesMedian(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "benchmarks.txt")
	data := strings.Join([]string{
		"goos: linux",
		benchmarkLine(coldBenchmark, 300, 30, 20, 3, 0),
		benchmarkLine(coldBenchmark, 100, 10, 8, 1, 0),
		benchmarkLine(coldBenchmark, 200, 20, 12, 2, 0),
		benchmarkLine(repeatedBenchmark, 30, 3, 0, 3, 0),
		benchmarkLine(repeatedBenchmark, 10, 1, 0, 1, 0),
		benchmarkLine(repeatedBenchmark, 20, 2, 0, 2, 0),
		benchmarkLine(warmBenchmark, 600, 60, 0, 6, 3),
		benchmarkLine(warmBenchmark, 400, 40, 0, 4, 1),
		benchmarkLine(warmBenchmark, 500, 50, 0, 5, 2),
	}, "\n")
	if err := os.WriteFile(path, []byte(data), fileMode); err != nil {
		t.Fatal(err)
	}

	got, err := loadBenchmarks(path)
	if err != nil {
		t.Fatal(err)
	}
	if got[coldBenchmark]["ns/op"] != 200 || got[coldBenchmark]["retained-B/op"] != 12 {
		t.Fatalf("cold metrics = %#v", got[coldBenchmark])
	}
	if got[repeatedBenchmark]["B/op"] != 2 {
		t.Fatalf("repeated metrics = %#v", got[repeatedBenchmark])
	}
	if got[warmBenchmark]["MB/s"] != 2 {
		t.Fatalf("warm metrics = %#v", got[warmBenchmark])
	}
}

func TestGenerateReport(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	previousCorpusPath := filepath.Join(directory, "previous.bin.gz")
	currentCorpusPath := filepath.Join(directory, "current.bin.gz")
	rebuiltCorpusPath := filepath.Join(directory, "rebuilt.bin.gz")
	writeCorpus(t, previousCorpusPath, previousCommit, "old.LICENSE")
	writeCorpus(t, currentCorpusPath, currentCommit, "new.LICENSE")
	writeCorpus(t, rebuiltCorpusPath, currentCommit, "new.LICENSE")

	previousConformancePath := filepath.Join(directory, "previous.json")
	currentConformancePath := filepath.Join(directory, "current.json")
	writeFile(t, previousConformancePath, `{
  "source_commit": "`+previousCommit+`",
  "cases": 3,
  "evaluated": 3,
  "passed": 1,
  "divergences": [{"path": "resolved"}, {"path": "unchanged"}]
}`)
	writeFile(t, currentConformancePath, `{
  "source_commit": "`+currentCommit+`",
  "cases": 3,
  "evaluated": 3,
  "passed": 1,
  "divergences": [{"path": "added"}, {"path": "unchanged"}]
}`)

	previousBenchmarksPath := filepath.Join(directory, "previous.txt")
	currentBenchmarksPath := filepath.Join(directory, "current.txt")
	writeBenchmarkFile(t, previousBenchmarksPath, 100)
	writeBenchmarkFile(t, currentBenchmarksPath, 110)

	report, err := generateReport(options{
		previousCorpus:      previousCorpusPath,
		corpus:              currentCorpusPath,
		rebuiltCorpus:       rebuiltCorpusPath,
		previousConformance: previousConformancePath,
		conformance:         currentConformancePath,
		previousBenchmarks:  previousBenchmarksPath,
		benchmarks:          currentBenchmarksPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Corpus regeneration report",
		"| ScanCode commit | `" + previousCommit + "` | `" + currentCommit + "` | — |",
		"Deterministic rebuild: **pass**",
		"### Cold startup",
		"### Repeated `New`",
		"### Warm `Match`",
		"| Time | 100.00 ns | 110.00 ns | +10.00% |",
		"<summary>Resolved divergences: 1</summary>",
		"- `resolved`",
		"<summary>New divergences: 1</summary>",
		"- `added`",
	} {
		if !bytes.Contains(report, []byte(want)) {
			t.Errorf("report does not contain %q:\n%s", want, report)
		}
	}
}

func TestEqualDecompressedCorporaRejectsDifferentRebuild(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	first := filepath.Join(directory, "first.bin.gz")
	second := filepath.Join(directory, "second.bin.gz")
	writeCorpus(t, first, currentCommit, "first.LICENSE")
	writeCorpus(t, second, currentCommit, "second.LICENSE")

	equal, err := equalDecompressedCorpora(first, second)
	if err != nil {
		t.Fatal(err)
	}
	if equal {
		t.Fatal("different corpus contents reported as equal")
	}
}

func TestRunRequiresInputs(t *testing.T) {
	t.Parallel()

	err := run(nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "-previous-corpus is required") {
		t.Fatalf("error = %v", err)
	}
}

func benchmarkLine(
	name string,
	nanoseconds,
	allocated,
	retained,
	allocations,
	throughput float64,
) string {
	line := name + "-8 1 " + formatRaw(nanoseconds) + " ns/op"
	if throughput != 0 {
		line += " " + formatRaw(throughput) + " MB/s"
	}
	if retained != 0 {
		line += " " + formatRaw(retained) + " retained-B/op"
	}
	return line + " " + formatRaw(allocated) + " B/op " + formatRaw(allocations) + " allocs/op"
}

func formatRaw(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func writeBenchmarkFile(t *testing.T, path string, nanoseconds float64) {
	t.Helper()
	data := strings.Join([]string{
		benchmarkLine(coldBenchmark, nanoseconds, 20, 10, 2, 0),
		benchmarkLine(repeatedBenchmark, nanoseconds, 20, 0, 2, 0),
		benchmarkLine(warmBenchmark, nanoseconds, 20, 0, 2, 5),
	}, "\n")
	writeFile(t, path, data)
}

func writeCorpus(t *testing.T, path, commit, ruleID string) {
	t.Helper()
	automaton, err := aho.Build([]aho.Pattern{{Tokens: []uint32{1}, Value: 0}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	index := corpus.Index{
		Info: corpus.Info{
			Version:      "test",
			RuleCount:    1,
			SourceCommit: commit,
		},
		Vocabulary: []string{"license"},
		Rules: []corpus.Rule{{
			ID:         ruleID,
			Expression: "test",
			Tokens:     []uint32{1},
			Flags:      corpus.FlagLicenseText,
			Relevance:  100,
		}},
		Automaton:    automaton,
		SPDXKeys:     map[string]string{"test": "test"},
		ReportingIDs: map[string]string{"test": "LicenseRef-scancode-test"},
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writeErr := corpus.Write(file, index)
	closeErr := file.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), fileMode); err != nil {
		t.Fatal(err)
	}
}
