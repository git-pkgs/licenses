// Command corpusreport compares a regenerated corpus with the previously
// checked-in corpus and writes a stable Markdown report.
package main

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/git-pkgs/licenses/internal/corpus"
)

const fileMode = 0o644

const (
	coldBenchmark     = "BenchmarkMatcherColdStart"
	repeatedBenchmark = "BenchmarkMatcherNewWarm"
	warmBenchmark     = "BenchmarkMatchCorpusHash"
)

var requiredBenchmarkMetrics = map[string][]string{
	coldBenchmark:     {"ns/op", "B/op", "retained-B/op", "allocs/op"},
	repeatedBenchmark: {"ns/op", "B/op", "allocs/op"},
	warmBenchmark:     {"ns/op", "MB/s", "B/op", "allocs/op"},
}

type options struct {
	previousCorpus      string
	corpus              string
	rebuiltCorpus       string
	previousConformance string
	conformance         string
	previousBenchmarks  string
	benchmarks          string
	output              string
}

type corpusSnapshot struct {
	commit    string
	hash      string
	ruleCount int
	size      int64
}

type conformanceSnapshot struct {
	SourceCommit string                  `json:"source_commit"`
	Cases        int                     `json:"cases"`
	Evaluated    int                     `json:"evaluated"`
	Passed       int                     `json:"passed"`
	Skipped      []json.RawMessage       `json:"skipped,omitempty"`
	Divergences  []conformanceDivergence `json:"divergences"`
}

type conformanceDivergence struct {
	Path string `json:"path"`
}

type benchmarkMetrics map[string]map[string]float64

type reportData struct {
	previousCorpus      corpusSnapshot
	corpus              corpusSnapshot
	previousConformance conformanceSnapshot
	conformance         conformanceSnapshot
	previousBenchmarks  benchmarkMetrics
	benchmarks          benchmarkMetrics
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "corpusreport:", err)
		os.Exit(1)
	}
}

func run(arguments []string, stdout, stderr io.Writer) error {
	var config options
	flags := flag.NewFlagSet("corpusreport", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&config.previousCorpus, "previous-corpus", "", "path to the checked-in corpus")
	flags.StringVar(&config.corpus, "corpus", "", "path to the regenerated corpus")
	flags.StringVar(&config.rebuiltCorpus, "rebuilt-corpus", "", "path to a second regeneration")
	flags.StringVar(
		&config.previousConformance,
		"previous-conformance",
		"",
		"path to the checked-in conformance baseline",
	)
	flags.StringVar(&config.conformance, "conformance", "", "path to the regenerated conformance baseline")
	flags.StringVar(
		&config.previousBenchmarks,
		"previous-benchmarks",
		"",
		"path to checked-in corpus benchmark output",
	)
	flags.StringVar(&config.benchmarks, "benchmarks", "", "path to regenerated corpus benchmark output")
	flags.StringVar(&config.output, "output", "", "output Markdown path (default stdout)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if err := validateOptions(config); err != nil {
		return err
	}

	report, err := generateReport(config)
	if err != nil {
		return err
	}
	if config.output == "" {
		_, err = stdout.Write(report)
		return err
	}
	if err := os.WriteFile(config.output, report, fileMode); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

func validateOptions(config options) error {
	required := []struct {
		name  string
		value string
	}{
		{name: "previous-corpus", value: config.previousCorpus},
		{name: "corpus", value: config.corpus},
		{name: "rebuilt-corpus", value: config.rebuiltCorpus},
		{name: "previous-conformance", value: config.previousConformance},
		{name: "conformance", value: config.conformance},
		{name: "previous-benchmarks", value: config.previousBenchmarks},
		{name: "benchmarks", value: config.benchmarks},
	}
	for _, option := range required {
		if option.value == "" {
			return fmt.Errorf("-%s is required", option.name)
		}
	}
	return nil
}

func generateReport(config options) ([]byte, error) {
	previousCorpus, err := loadCorpusSnapshot(config.previousCorpus)
	if err != nil {
		return nil, fmt.Errorf("load previous corpus: %w", err)
	}
	currentCorpus, err := loadCorpusSnapshot(config.corpus)
	if err != nil {
		return nil, fmt.Errorf("load regenerated corpus: %w", err)
	}
	deterministic, err := equalDecompressedCorpora(config.corpus, config.rebuiltCorpus)
	if err != nil {
		return nil, fmt.Errorf("compare deterministic rebuild: %w", err)
	}
	if !deterministic {
		return nil, errors.New("second regeneration differs from the regenerated corpus")
	}
	previousConformance, err := loadConformance(config.previousConformance)
	if err != nil {
		return nil, fmt.Errorf("load previous conformance baseline: %w", err)
	}
	currentConformance, err := loadConformance(config.conformance)
	if err != nil {
		return nil, fmt.Errorf("load regenerated conformance baseline: %w", err)
	}
	if previousConformance.SourceCommit != previousCorpus.commit {
		return nil, fmt.Errorf(
			"previous conformance commit %s does not match corpus commit %s",
			previousConformance.SourceCommit,
			previousCorpus.commit,
		)
	}
	if currentConformance.SourceCommit != currentCorpus.commit {
		return nil, fmt.Errorf(
			"regenerated conformance commit %s does not match corpus commit %s",
			currentConformance.SourceCommit,
			currentCorpus.commit,
		)
	}
	previousBenchmarks, err := loadBenchmarks(config.previousBenchmarks)
	if err != nil {
		return nil, fmt.Errorf("load previous benchmarks: %w", err)
	}
	currentBenchmarks, err := loadBenchmarks(config.benchmarks)
	if err != nil {
		return nil, fmt.Errorf("load regenerated benchmarks: %w", err)
	}

	return renderReport(reportData{
		previousCorpus:      previousCorpus,
		corpus:              currentCorpus,
		previousConformance: previousConformance,
		conformance:         currentConformance,
		previousBenchmarks:  previousBenchmarks,
		benchmarks:          currentBenchmarks,
	}), nil
}

func loadCorpusSnapshot(path string) (corpusSnapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return corpusSnapshot{}, err
	}
	defer func() { _ = file.Close() }()

	digest := sha256.New()
	size, err := io.Copy(digest, file)
	if err != nil {
		return corpusSnapshot{}, fmt.Errorf("hash corpus: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return corpusSnapshot{}, fmt.Errorf("rewind corpus: %w", err)
	}
	index, err := corpus.Read(file)
	if err != nil {
		return corpusSnapshot{}, err
	}
	return corpusSnapshot{
		commit:    index.Info.SourceCommit,
		hash:      hex.EncodeToString(digest.Sum(nil)),
		ruleCount: len(index.Rules),
		size:      size,
	}, nil
}

func equalDecompressedCorpora(first, second string) (bool, error) {
	firstHash, err := decompressedCorpusHash(first)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", first, err)
	}
	secondHash, err := decompressedCorpusHash(second)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", second, err)
	}
	return firstHash == secondHash, nil
}

func decompressedCorpusHash(path string) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	file, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return result, err
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, reader); err != nil {
		_ = reader.Close()
		return result, err
	}
	if err := reader.Close(); err != nil {
		return result, err
	}
	copy(result[:], digest.Sum(nil))
	return result, nil
}

func loadConformance(path string) (conformanceSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return conformanceSnapshot{}, err
	}
	var snapshot conformanceSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return conformanceSnapshot{}, err
	}
	if snapshot.SourceCommit == "" {
		return conformanceSnapshot{}, errors.New("missing source_commit")
	}
	if snapshot.Evaluated < 0 || snapshot.Passed < 0 || snapshot.Passed > snapshot.Evaluated {
		return conformanceSnapshot{}, errors.New("invalid conformance counts")
	}
	for index, divergence := range snapshot.Divergences {
		if divergence.Path == "" {
			return conformanceSnapshot{}, fmt.Errorf("divergence %d has no path", index)
		}
	}
	return snapshot, nil
}

func loadBenchmarks(path string) (benchmarkMetrics, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	samples := make(map[string]map[string][]float64, len(requiredBenchmarkMetrics))
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		name := trimBenchmarkCPUSuffix(fields[0])
		if _, wanted := requiredBenchmarkMetrics[name]; !wanted {
			continue
		}
		if _, err := strconv.ParseUint(fields[1], 10, 64); err != nil {
			continue
		}
		if samples[name] == nil {
			samples[name] = make(map[string][]float64)
		}
		for index := 2; index+1 < len(fields); index += 2 {
			value, err := strconv.ParseFloat(fields[index], 64)
			if err != nil {
				continue
			}
			unit := fields[index+1]
			samples[name][unit] = append(samples[name][unit], value)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	metrics := make(benchmarkMetrics, len(requiredBenchmarkMetrics))
	for name, units := range requiredBenchmarkMetrics {
		metrics[name] = make(map[string]float64, len(units))
		for _, unit := range units {
			values := samples[name][unit]
			if len(values) == 0 {
				return nil, fmt.Errorf("missing %s %s", name, unit)
			}
			metrics[name][unit] = median(values)
		}
	}
	return metrics, nil
}

func trimBenchmarkCPUSuffix(name string) string {
	separator := strings.LastIndexByte(name, '-')
	if separator == -1 {
		return name
	}
	if _, err := strconv.ParseUint(name[separator+1:], 10, 32); err != nil {
		return name
	}
	return name[:separator]
}

func median(values []float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 != 0 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}

func renderReport(report reportData) []byte {
	var output strings.Builder
	output.WriteString("Update the embedded license corpus and conformance baseline from the latest ")
	output.WriteString("`aboutcode-org/scancode-toolkit` `develop` branch.\n\n")
	output.WriteString("## Corpus regeneration report\n\n")
	output.WriteString("### Corpus\n\n")
	output.WriteString("| Metric | Previous | Regenerated | Change |\n")
	output.WriteString("|:---|:---|:---|---:|\n")
	fmt.Fprintf(
		&output,
		"| ScanCode commit | `%s` | `%s` | — |\n",
		report.previousCorpus.commit,
		report.corpus.commit,
	)
	fmt.Fprintf(
		&output,
		"| Corpus SHA-256 | `%s` | `%s` | — |\n",
		report.previousCorpus.hash,
		report.corpus.hash,
	)
	fmt.Fprintf(
		&output,
		"| Rules | %s | %s | %s |\n",
		formatInteger(int64(report.previousCorpus.ruleCount)),
		formatInteger(int64(report.corpus.ruleCount)),
		formatSignedInteger(int64(report.corpus.ruleCount-report.previousCorpus.ruleCount)),
	)
	fmt.Fprintf(
		&output,
		"| Embedded size | %s | %s | %s |\n\n",
		formatSize(report.previousCorpus.size),
		formatSize(report.corpus.size),
		formatSizeChange(report.previousCorpus.size, report.corpus.size),
	)
	output.WriteString("Deterministic rebuild: **pass** — independently regenerated corpus contents are identical.\n\n")

	writeBenchmarkSection(
		&output,
		"Cold startup",
		"A cold `New` includes corpus decompression and matcher construction.",
		coldBenchmark,
		[]metricRow{
			{label: "Time", unit: "ns/op", format: formatDuration},
			{label: "Allocated bytes", unit: "B/op", format: formatBytes},
			{label: "Retained heap", unit: "retained-B/op", format: formatBytes},
			{label: "Allocations", unit: "allocs/op", format: formatCount},
		},
		report,
	)
	writeBenchmarkSection(
		&output,
		"Repeated `New`",
		"Repeated `New` reuses the process-wide matcher engine.",
		repeatedBenchmark,
		[]metricRow{
			{label: "Time", unit: "ns/op", format: formatDuration},
			{label: "Allocated bytes", unit: "B/op", format: formatBytes},
			{label: "Allocations", unit: "allocs/op", format: formatCount},
		},
		report,
	)
	writeBenchmarkSection(
		&output,
		"Warm `Match`",
		"Warm matching reuses one matcher and runs every non-empty corpus rule as input.",
		warmBenchmark,
		[]metricRow{
			{label: "Time per corpus pass", unit: "ns/op", format: formatDuration},
			{label: "Throughput", unit: "MB/s", format: formatThroughput},
			{label: "Allocated bytes", unit: "B/op", format: formatBytes},
			{label: "Allocations", unit: "allocs/op", format: formatCount},
		},
		report,
	)

	output.WriteString("### Conformance\n\n")
	output.WriteString("| Metric | Previous | Regenerated | Change |\n")
	output.WriteString("|:---|---:|---:|---:|\n")
	fmt.Fprintf(
		&output,
		"| Passed | %s | %s | %s |\n",
		formatConformance(report.previousConformance),
		formatConformance(report.conformance),
		formatSignedInteger(int64(report.conformance.Passed-report.previousConformance.Passed)),
	)
	fmt.Fprintf(
		&output,
		"| Divergences | %s | %s | %s |\n",
		formatInteger(int64(len(report.previousConformance.Divergences))),
		formatInteger(int64(len(report.conformance.Divergences))),
		formatSignedInteger(
			int64(len(report.conformance.Divergences)-len(report.previousConformance.Divergences)),
		),
	)
	fmt.Fprintf(
		&output,
		"| Skipped | %s | %s | %s |\n\n",
		formatInteger(int64(len(report.previousConformance.Skipped))),
		formatInteger(int64(len(report.conformance.Skipped))),
		formatSignedInteger(int64(len(report.conformance.Skipped)-len(report.previousConformance.Skipped))),
	)
	resolved, added := divergenceChanges(report.previousConformance, report.conformance)
	writeDivergenceChanges(&output, "Resolved divergences", resolved)
	writeDivergenceChanges(&output, "New divergences", added)

	fmt.Fprintf(
		&output,
		"\nBenchmarks are medians of five samples on Go %s (%s/%s). Cold startup uses one iteration per sample; repeated `New` and warm `Match` use one second per sample.\n",
		strings.TrimPrefix(runtime.Version(), "go"),
		runtime.GOOS,
		runtime.GOARCH,
	)
	return []byte(output.String())
}

type metricRow struct {
	label  string
	unit   string
	format func(float64) string
}

func writeBenchmarkSection(
	output *strings.Builder,
	title,
	description,
	benchmark string,
	rows []metricRow,
	report reportData,
) {
	fmt.Fprintf(output, "### %s\n\n%s\n\n", title, description)
	output.WriteString("| Metric | Previous | Regenerated | Change |\n")
	output.WriteString("|:---|---:|---:|---:|\n")
	for _, row := range rows {
		previous := report.previousBenchmarks[benchmark][row.unit]
		current := report.benchmarks[benchmark][row.unit]
		fmt.Fprintf(
			output,
			"| %s | %s | %s | %s |\n",
			row.label,
			row.format(previous),
			row.format(current),
			formatPercentChange(previous, current),
		)
	}
	output.WriteByte('\n')
}

func divergenceChanges(
	previous,
	current conformanceSnapshot,
) (resolved, added []string) {
	previousPaths := make(map[string]struct{}, len(previous.Divergences))
	for _, divergence := range previous.Divergences {
		previousPaths[divergence.Path] = struct{}{}
	}
	currentPaths := make(map[string]struct{}, len(current.Divergences))
	for _, divergence := range current.Divergences {
		currentPaths[divergence.Path] = struct{}{}
	}
	for path := range previousPaths {
		if _, remains := currentPaths[path]; !remains {
			resolved = append(resolved, path)
		}
	}
	for path := range currentPaths {
		if _, existed := previousPaths[path]; !existed {
			added = append(added, path)
		}
	}
	sort.Strings(resolved)
	sort.Strings(added)
	return resolved, added
}

func writeDivergenceChanges(output *strings.Builder, label string, paths []string) {
	if len(paths) == 0 {
		fmt.Fprintf(output, "- %s: 0\n", label)
		return
	}
	fmt.Fprintf(output, "\n<details>\n<summary>%s: %d</summary>\n\n", label, len(paths))
	for _, path := range paths {
		fmt.Fprintf(output, "- `%s`\n", path)
	}
	output.WriteString("\n</details>\n\n")
}

func formatConformance(snapshot conformanceSnapshot) string {
	rate := float64(0)
	if snapshot.Evaluated != 0 {
		rate = float64(snapshot.Passed) / float64(snapshot.Evaluated) * 100
	}
	return fmt.Sprintf(
		"%s / %s (%.2f%%)",
		formatInteger(int64(snapshot.Passed)),
		formatInteger(int64(snapshot.Evaluated)),
		rate,
	)
}

func formatDuration(nanoseconds float64) string {
	switch {
	case nanoseconds >= 1e9:
		return fmt.Sprintf("%.2f s", nanoseconds/1e9)
	case nanoseconds >= 1e6:
		return fmt.Sprintf("%.2f ms", nanoseconds/1e6)
	case nanoseconds >= 1e3:
		return fmt.Sprintf("%.2f µs", nanoseconds/1e3)
	default:
		return fmt.Sprintf("%.2f ns", nanoseconds)
	}
}

func formatBytes(bytes float64) string {
	return formatSize(int64(bytes + 0.5))
}

func formatSize(bytes int64) string {
	switch {
	case bytes >= 1<<20:
		return fmt.Sprintf("%.2f MiB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.2f KiB", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%s B", formatInteger(bytes))
	}
}

func formatCount(value float64) string {
	return formatInteger(int64(value + 0.5))
}

func formatThroughput(value float64) string {
	return fmt.Sprintf("%.2f MB/s", value)
}

func formatPercentChange(previous, current float64) string {
	if previous == 0 {
		return "—"
	}
	return fmt.Sprintf("%+.2f%%", (current-previous)/previous*100)
}

func formatSizeChange(previous, current int64) string {
	if previous == 0 {
		return "—"
	}
	return fmt.Sprintf(
		"%s (%+.2f%%)",
		formatSignedSize(current-previous),
		float64(current-previous)/float64(previous)*100,
	)
}

func formatSignedSize(value int64) string {
	if value == 0 {
		return "0 B"
	}
	prefix := "+"
	if value < 0 {
		prefix = "−"
		value = -value
	}
	return prefix + formatSize(value)
}

func formatSignedInteger(value int64) string {
	if value == 0 {
		return "0"
	}
	prefix := "+"
	if value < 0 {
		prefix = "−"
		value = -value
	}
	return prefix + formatInteger(value)
}

func formatInteger(value int64) string {
	digits := strconv.FormatInt(value, 10)
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	return digits
}
