package licenses

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

const archiveRegressionPath = "testdata/archive_regressions"

type archiveRegressionCorpus struct {
	Schema          int                     `json:"schema"`
	Sample          string                  `json:"sample"`
	ScannerSource   string                  `json:"scanner_source"`
	LicenseeVersion string                  `json:"licensee_version"`
	Cases           []archiveRegressionCase `json:"cases"`
}

type archiveRegressionCase struct {
	ID                 string                    `json:"id"`
	Ecosystem          string                    `json:"ecosystem"`
	Package            string                    `json:"package"`
	Version            string                    `json:"version"`
	ArchiveURL         string                    `json:"archive_url"`
	ArchiveSHA256      string                    `json:"archive_sha256"`
	ArchivePath        string                    `json:"archive_path"`
	Fixture            string                    `json:"fixture"`
	ContentSHA256      string                    `json:"content_sha256"`
	SourceLicense      string                    `json:"source_license"`
	Category           string                    `json:"category"`
	ComparisonNote     string                    `json:"comparison_note,omitempty"`
	LicenseeExpression string                    `json:"licensee_expression,omitempty"`
	Expected           archiveRegressionExpected `json:"expected"`
}

type archiveRegressionExpected struct {
	Encoding          string                       `json:"encoding"`
	Roles             []string                     `json:"roles"`
	Detections        []archiveRegressionDetection `json:"detections"`
	ClueCount         int                          `json:"clue_count"`
	MatchedTextSHA256 []string                     `json:"matched_text_sha256"`
}

type archiveRegressionDetection struct {
	Expression     string         `json:"expression"`
	Identification Identification `json:"identification"`
}

type archiveRegressionSample struct {
	Schema        int                     `json:"schema"`
	ScannerSource string                  `json:"scanner_source"`
	Comparator    archiveSampleComparator `json:"comparator"`
	Selection     archiveSampleSelection  `json:"selection"`
	Summary       archiveSampleSummary    `json:"summary"`
	Misses        []archiveSampleMiss     `json:"useful_exact_misses"`
	Releases      []archiveSampleRelease  `json:"releases"`
}

type archiveSampleComparator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type archiveSampleSelection struct {
	Seed        string                    `json:"seed"`
	Method      string                    `json:"method"`
	Populations []archiveSamplePopulation `json:"populations"`
}

type archiveSamplePopulation struct {
	Ecosystem string `json:"ecosystem"`
	SourceURL string `json:"source_url"`
	SHA256    string `json:"sha256"`
	Records   int    `json:"records"`
	Selected  int    `json:"selected"`
}

type archiveSampleSummary struct {
	SelectedReleases          int            `json:"selected_releases"`
	ProcessedReleases         int            `json:"processed_releases"`
	ReleaseOutcomes           map[string]int `json:"release_outcomes"`
	InputOccurrences          int            `json:"input_occurrences"`
	UniqueInputHashes         int            `json:"unique_input_hashes"`
	MatchedTextOccurrences    int            `json:"matched_text_occurrences"`
	UniqueMatchedTextHashes   int            `json:"unique_matched_text_hashes"`
	ComparisonOccurrences     int            `json:"comparison_occurrences"`
	UniqueSameFileComparisons int            `json:"unique_same_file_comparisons"`
	ComparisonStates          map[string]int `json:"comparison_states"`
	ComparatorMatchers        map[string]int `json:"comparator_matchers"`
	UsefulExactMisses         int            `json:"useful_exact_misses"`
	ScanErrors                int            `json:"scan_errors"`
}

type archiveSampleMiss struct {
	Archive    string  `json:"archive"`
	Path       string  `json:"path"`
	SHA256     string  `json:"sha256"`
	Licensee   string  `json:"licensee"`
	Confidence float64 `json:"confidence"`
}

type archiveSampleRelease struct {
	ID            string `json:"id"`
	Ecosystem     string `json:"ecosystem"`
	Package       string `json:"package"`
	Version       string `json:"version"`
	Status        string `json:"status"`
	ArchiveURL    string `json:"archive_url"`
	ArchiveSHA256 string `json:"archive_sha256"`
}

func TestArchiveRegressionCorpus(t *testing.T) {
	corpus := readArchiveRegressionCorpus(t)
	matcher, err := New(WithMatchedText())
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultScanOptions()
	options.NoDefaultSkip = true
	options.IncludeLegalFiles = true
	options.Workers = 2
	options.MaxFiles = 10
	options.MaxFileSize = 1 << 20
	options.MaxDepth = 16

	for _, testCase := range corpus.Cases {
		t.Run(testCase.ID, func(t *testing.T) {
			testArchiveRegressionCase(t, matcher, options, testCase)
		})
	}
}

func testArchiveRegressionCase(t *testing.T, matcher *Matcher, options ScanOptions, testCase archiveRegressionCase) {
	t.Helper()
	root := filepath.Join(archiveRegressionPath, "files", testCase.ID)
	input, err := os.ReadFile(filepath.Join(archiveRegressionPath, testCase.Fixture))
	if err != nil {
		t.Fatal(err)
	}
	if got := sha256Hex(input); got != testCase.ContentSHA256 {
		t.Fatalf("content SHA-256 = %s, want %s", got, testCase.ContentSHA256)
	}
	report, err := ScanRepository(context.Background(), matcher, root, options)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.FilesVisited != 1 || report.Summary.FilesScanned != 1 ||
		report.Summary.ErrorCount != 0 || report.Summary.Truncated || len(report.Files) != 1 {
		t.Fatalf("incomplete report: %+v", report)
	}
	file := report.Files[0]
	if file.Path != testCase.ArchivePath || file.SHA256 != testCase.ContentSHA256 ||
		file.Encoding != testCase.Expected.Encoding || !slices.Equal(file.Roles, testCase.Expected.Roles) {
		t.Fatalf("file metadata = %+v", file)
	}
	gotDetections := make([]archiveRegressionDetection, 0, len(file.Detections))
	var matchedTextHashes []string
	for _, detection := range file.Detections {
		gotDetections = append(gotDetections, archiveRegressionDetection{
			Expression: detection.Expression, Identification: detection.Identification,
		})
		for _, match := range detection.Matches {
			if match.Matched == "" {
				t.Fatalf("match %s has no retained text", match.RuleID)
			}
			matchedTextHashes = append(matchedTextHashes, sha256Hex([]byte(match.Matched)))
		}
	}
	sortDetections := func(detections []archiveRegressionDetection) {
		sort.Slice(detections, func(i, j int) bool {
			if detections[i].Expression == detections[j].Expression {
				return detections[i].Identification < detections[j].Identification
			}
			return detections[i].Expression < detections[j].Expression
		})
	}
	sortDetections(gotDetections)
	wantDetections := slices.Clone(testCase.Expected.Detections)
	sortDetections(wantDetections)
	slices.Sort(matchedTextHashes)
	matchedTextHashes = slices.Compact(matchedTextHashes)
	if !slices.Equal(gotDetections, wantDetections) ||
		len(file.Clues) != testCase.Expected.ClueCount ||
		!slices.Equal(matchedTextHashes, testCase.Expected.MatchedTextSHA256) {
		t.Fatalf("result changed:\n detections = %+v\n clues = %d\n matched text = %v",
			gotDetections, len(file.Clues), matchedTextHashes)
	}
}

func TestArchiveRegressionCorpusMetadata(t *testing.T) {
	corpus := readArchiveRegressionCorpus(t)
	if corpus.Schema != 1 || corpus.Sample == "" || corpus.LicenseeVersion == "" ||
		!validHex(corpus.ScannerSource, sha1.Size) || len(corpus.Cases) != 10 {
		t.Fatalf("incomplete corpus metadata: %+v", corpus)
	}
	wantCategories := []string{
		"dual-license", "empty-result", "encoding", "exact-agreement", "noassertion",
		"partial-identification", "short-reference-policy", "spdx-tag", "useful-exact-miss",
	}
	var categories []string
	inputHashes := make(map[string]string)
	for _, testCase := range corpus.Cases {
		parsed, err := url.Parse(testCase.ArchiveURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			t.Errorf("%s has invalid archive URL %q", testCase.ID, testCase.ArchiveURL)
		}
		if testCase.ID == "" || testCase.Ecosystem == "" || testCase.Package == "" ||
			testCase.Version == "" || testCase.ArchivePath == "" || testCase.Fixture == "" ||
			testCase.SourceLicense == "" || testCase.Category == "" ||
			!validHex(testCase.ArchiveSHA256, sha256.Size) || !validHex(testCase.ContentSHA256, sha256.Size) {
			t.Errorf("%s has incomplete provenance", testCase.ID)
		}
		if previous := inputHashes[testCase.ContentSHA256]; previous != "" {
			t.Errorf("%s duplicates input content from %s", testCase.ID, previous)
		}
		inputHashes[testCase.ContentSHA256] = testCase.ID
		categories = append(categories, testCase.Category)
		if !slices.IsSorted(testCase.Expected.MatchedTextSHA256) ||
			len(testCase.Expected.MatchedTextSHA256) != len(slices.Compact(slices.Clone(testCase.Expected.MatchedTextSHA256))) {
			t.Errorf("%s has ungrouped matched-text hashes", testCase.ID)
		}
	}
	slices.Sort(categories)
	categories = slices.Compact(categories)
	if !slices.Equal(categories, wantCategories) {
		t.Fatalf("categories = %v, want %v", categories, wantCategories)
	}
}

func TestArchiveRegressionSampleMetadata(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(archiveRegressionPath, "sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sample archiveRegressionSample
	if err := json.Unmarshal(data, &sample); err != nil {
		t.Fatal(err)
	}
	testArchiveSampleHeader(t, sample)
	testArchiveSampleSummary(t, sample)
	testArchiveSampleDetails(t, sample)
}

func testArchiveSampleHeader(t *testing.T, sample archiveRegressionSample) {
	t.Helper()
	corpus := readArchiveRegressionCorpus(t)
	if sample.Schema != 1 || sample.ScannerSource != corpus.ScannerSource {
		t.Fatalf("incomplete sample metadata: %+v", sample)
	}
	if sample.Comparator.Name != "Licensee" || sample.Comparator.Version != corpus.LicenseeVersion {
		t.Fatalf("incomplete comparator metadata: %+v", sample.Comparator)
	}
	if sample.Selection.Seed == "" || sample.Selection.Method == "" || len(sample.Selection.Populations) != 5 {
		t.Fatalf("incomplete selection metadata: %+v", sample.Selection)
	}
	selected := 0
	for _, population := range sample.Selection.Populations {
		parsed, err := url.Parse(population.SourceURL)
		if population.Ecosystem == "" || err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
			!validHex(population.SHA256, sha256.Size) || population.Records != 100 || population.Selected != 20 {
			t.Errorf("incomplete population metadata: %+v", population)
		}
		selected += population.Selected
	}
	if selected != sample.Summary.SelectedReleases {
		t.Fatalf("population selection count = %d, want %d", selected, sample.Summary.SelectedReleases)
	}
}

func testArchiveSampleSummary(t *testing.T, sample archiveRegressionSample) {
	t.Helper()
	summary := sample.Summary
	if summary.SelectedReleases != 100 || summary.ProcessedReleases != 100 ||
		len(sample.Releases) != summary.SelectedReleases || sumCounts(summary.ReleaseOutcomes) != summary.ProcessedReleases {
		t.Fatalf("inconsistent release summary: %+v", summary)
	}
	if summary.UniqueInputHashes > summary.InputOccurrences ||
		summary.UniqueMatchedTextHashes > summary.MatchedTextOccurrences {
		t.Fatalf("ungrouped content counts: %+v", summary)
	}
	if sumCounts(summary.ComparisonStates) != summary.UniqueSameFileComparisons ||
		sumCounts(summary.ComparatorMatchers) != summary.ComparisonOccurrences {
		t.Fatalf("inconsistent comparison summary: %+v", summary)
	}
	if summary.UsefulExactMisses != 3 || summary.ComparatorMatchers["dice"] != summary.UsefulExactMisses ||
		len(sample.Misses) != summary.UsefulExactMisses {
		t.Fatalf("inconsistent useful exact misses: %+v", summary)
	}
}

func testArchiveSampleDetails(t *testing.T, sample archiveRegressionSample) {
	t.Helper()
	releases := make(map[string]struct{}, len(sample.Releases))
	for _, release := range sample.Releases {
		parsed, err := url.Parse(release.ArchiveURL)
		if release.ID == "" || release.Ecosystem == "" || release.Package == "" || release.Version == "" ||
			release.Status == "" || err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
			!validHex(release.ArchiveSHA256, sha256.Size) {
			t.Errorf("%s has incomplete release metadata", release.ID)
		}
		if _, exists := releases[release.ID]; exists {
			t.Errorf("duplicate release %s", release.ID)
		}
		releases[release.ID] = struct{}{}
	}
	for _, miss := range sample.Misses {
		if miss.Archive == "" || miss.Path == "" || miss.Licensee == "" ||
			!validHex(miss.SHA256, sha256.Size) || miss.Confidence < 98 {
			t.Errorf("incomplete useful exact miss: %+v", miss)
		}
	}
}

func readArchiveRegressionCorpus(t *testing.T) archiveRegressionCorpus {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(archiveRegressionPath, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus archiveRegressionCorpus
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	return corpus
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func validHex(value string, bytes int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == bytes
}

func sumCounts(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}
