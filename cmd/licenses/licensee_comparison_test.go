package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	licenses "github.com/git-pkgs/licenses"
	"github.com/git-pkgs/licenses/internal/corpus"
	gitspdx "github.com/git-pkgs/spdx"
)

type licenseeReport struct {
	Licenses     []licenseeLicense     `json:"licenses"`
	MatchedFiles []licenseeMatchedFile `json:"matched_files"`
}

type licenseeLicense struct {
	Key    string `json:"key"`
	SPDXID string `json:"spdx_id"`
}

type licenseeMatchedFile struct {
	Filename       string `json:"filename"`
	MatchedLicense string `json:"matched_license"`
	Matcher        struct {
		Name string `json:"name"`
	} `json:"matcher"`
}

type licenseeComparison struct {
	ProjectText    []string
	ProjectClues   []string
	OtherText      []string
	OtherClues     []string
	LicenseeText   []string
	LicenseeFields []string
}

func TestCompareLicenseeRepositories(t *testing.T) {
	value := os.Getenv("LICENSES_BENCH_REPOS")
	if value == "" {
		t.Skip("set LICENSES_BENCH_REPOS to a path-list of repositories")
	}
	licenseeBinary := os.Getenv("LICENSEE_BENCH_BIN")
	if licenseeBinary == "" {
		var err error
		licenseeBinary, err = exec.LookPath("licensee")
		if err != nil {
			t.Skip("licensee is not installed and LICENSEE_BENCH_BIN is unset")
		}
	}
	index, err := corpus.Load()
	if err != nil {
		t.Fatal(err)
	}
	spdxKeys := index.SPDXKeys
	index = corpus.Index{}
	matcher, err := licenses.New()
	if err != nil {
		t.Fatal(err)
	}
	options := scanOptions{
		MaxDepth:    defaultMaxDepth,
		MaxFiles:    defaultMaxFiles,
		MaxFileSize: defaultMaxFileSize,
		Workers:     defaultWorkerCount(),
	}

	var repositories, exactAgreements int
	for _, root := range filepath.SplitList(value) {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		repositories++
		ours, err := scanRepository(
			context.Background(),
			matcher,
			root,
			options,
			testScannerVersion,
		)
		if err != nil {
			t.Fatalf("scan %s: %v", root, err)
		}
		var raw strings.Builder
		if _, err := runLicenseeCLI(
			context.Background(),
			licenseeBinary,
			root,
			&raw,
		); err != nil {
			t.Fatal(err)
		}
		var licensee licenseeReport
		if raw.Len() > 0 {
			if err := json.Unmarshal([]byte(raw.String()), &licensee); err != nil {
				t.Fatalf("decode Licensee output for %s: %v", root, err)
			}
		}

		comparison := compareLicenseResults(ours, licensee, spdxKeys)
		if slices.Equal(comparison.ProjectText, comparison.LicenseeText) {
			exactAgreements++
		}
		t.Logf(
			"%s\n  licenses project text: %s\n"+
				"  licenses project clues: %s\n"+
				"  licenses other text: %s\n"+
				"  licenses other clues: %s\n"+
				"  Licensee text: %s\n  Licensee manifest fields: %s",
			root,
			formatComparisonValues(comparison.ProjectText),
			formatComparisonValues(comparison.ProjectClues),
			formatComparisonValues(comparison.OtherText),
			formatComparisonValues(comparison.OtherClues),
			formatComparisonValues(comparison.LicenseeText),
			formatComparisonValues(comparison.LicenseeFields),
		)
	}
	if repositories == 0 {
		t.Fatal("LICENSES_BENCH_REPOS contains no repository paths")
	}
	t.Logf(
		"text identifier agreement: %d/%d repositories",
		exactAgreements,
		repositories,
	)
}

func compareLicenseResults(
	ours scanReport,
	licensee licenseeReport,
	spdxKeys map[string]string,
) licenseeComparison {
	var result licenseeComparison
	for _, file := range ours.Files {
		projectFile := isComparisonProjectFile(file.Path)
		for _, detection := range file.Detections {
			for _, match := range detection.Matches {
				if projectFile {
					result.ProjectText = append(
						result.ProjectText,
						match.LicenseIDs...,
					)
				} else {
					result.OtherText = append(
						result.OtherText,
						match.LicenseIDs...,
					)
				}
			}
		}
		for _, clue := range file.Clues {
			if projectFile {
				result.ProjectClues = append(
					result.ProjectClues,
					clue.LicenseIDs...,
				)
			} else {
				result.OtherClues = append(
					result.OtherClues,
					clue.LicenseIDs...,
				)
			}
		}
	}
	for _, file := range licensee.MatchedFiles {
		expression := normalizeLicenseeExpression(file.MatchedLicense, spdxKeys)
		if expression == "" {
			continue
		}
		switch file.Matcher.Name {
		case "exact", "dice", "copyright", "reference":
			result.LicenseeText = append(result.LicenseeText, expression)
		default:
			result.LicenseeFields = append(result.LicenseeFields, expression)
		}
	}
	result.ProjectText = sortedUnique(result.ProjectText)
	result.ProjectClues = sortedUnique(result.ProjectClues)
	result.OtherText = sortedUnique(result.OtherText)
	result.OtherClues = sortedUnique(result.OtherClues)
	result.LicenseeText = sortedUnique(result.LicenseeText)
	result.LicenseeFields = sortedUnique(result.LicenseeFields)
	return result
}

func isComparisonProjectFile(filePath string) bool {
	cleaned := strings.TrimPrefix(filepath.ToSlash(filePath), "./")
	if strings.Contains(cleaned, "/") {
		return false
	}
	if isLegalFile(filePath) {
		return true
	}
	name := strings.ToLower(pathpkg.Base(cleaned))
	return name == "readme" || strings.HasPrefix(name, "readme.")
}

func normalizeLicenseeExpression(
	raw string,
	spdxKeys map[string]string,
) string {
	expression, err := gitspdx.ParseStrict(raw)
	if err != nil {
		return raw
	}
	return gitspdx.RewriteIdentifiers(expression, func(identifier string) string {
		if key := spdxKeys[strings.ToLower(identifier)]; key != "" {
			return key
		}
		return identifier
	})
}

func sortedUnique(values []string) []string {
	slices.Sort(values)
	return slices.Compact(values)
}

func formatComparisonValues(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	const sampleSize = 20
	if len(values) <= sampleSize {
		return strings.Join(values, ", ")
	}
	return fmt.Sprintf(
		"%s ... (%d total)",
		strings.Join(values[:sampleSize], ", "),
		len(values),
	)
}

func TestCompareLicenseResultsSeparatesLicenseeSources(t *testing.T) {
	var ours scanReport
	ours.Files = []fileRecord{{
		Path: "LICENSE",
		Detections: []detectionRecord{{
			Matches: []matchRecord{{LicenseIDs: []string{"mit"}}},
		}},
		Clues: []matchRecord{{LicenseIDs: []string{"ruby"}}},
	}}
	exact := licenseeMatchedFile{MatchedLicense: "MIT"}
	exact.Matcher.Name = "exact"
	dice := licenseeMatchedFile{MatchedLicense: "BSD-3-Clause"}
	dice.Matcher.Name = "dice"
	declared := licenseeMatchedFile{MatchedLicense: "Apache-2.0"}
	declared.Matcher.Name = "gemspec"
	licensee := licenseeReport{
		MatchedFiles: []licenseeMatchedFile{exact, dice, declared},
	}

	got := compareLicenseResults(
		ours,
		licensee,
		map[string]string{
			"mit":          "mit",
			"apache-2.0":   "apache-2.0",
			"bsd-3-clause": "bsd-new",
		},
	)
	if !slices.Equal(got.ProjectText, []string{"mit"}) {
		t.Errorf("project text = %v, want [mit]", got.ProjectText)
	}
	if !slices.Equal(got.ProjectClues, []string{"ruby"}) {
		t.Errorf("project clues = %v, want [ruby]", got.ProjectClues)
	}
	if !slices.Equal(got.LicenseeText, []string{"bsd-new", "mit"}) {
		t.Errorf("Licensee text = %v, want [bsd-new mit]", got.LicenseeText)
	}
	if !slices.Equal(got.LicenseeFields, []string{"apache-2.0"}) {
		t.Errorf(
			"Licensee manifest fields = %v, want [apache-2.0]",
			got.LicenseeFields,
		)
	}
}

func TestIsComparisonProjectFile(t *testing.T) {
	tests := map[string]bool{
		"LICENSE":              true,
		"LICENSES/MIT.txt":     false,
		"docs/README.md":       false,
		"README":               true,
		"docs/licensing.md":    false,
		"src/license_check.go": false,
	}
	for filePath, want := range tests {
		if got := isComparisonProjectFile(filePath); got != want {
			t.Errorf(
				"isComparisonProjectFile(%q) = %t, want %t",
				filePath,
				got,
				want,
			)
		}
	}
}

func TestNormalizeLicenseeExpression(t *testing.T) {
	keys := map[string]string{
		"mit":          "mit",
		"bsd-3-clause": "bsd-new",
	}
	got := normalizeLicenseeExpression("MIT OR BSD-3-Clause", keys)
	if got != "mit OR bsd-new" {
		t.Errorf("normalized expression = %q, want %q", got, "mit OR bsd-new")
	}
}

func TestNormalizeMaxRSS(t *testing.T) {
	const value = int64(123)
	if got := normalizeMaxRSS("linux", value); got != value*1024 {
		t.Errorf("Linux max RSS = %d, want %d", got, value*1024)
	}
	if got := normalizeMaxRSS("darwin", value); got != value {
		t.Errorf("Darwin max RSS = %d, want %d", got, value)
	}
}
