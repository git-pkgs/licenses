package main

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	licenses "github.com/git-pkgs/licenses"
	"github.com/git-pkgs/magic"
)

func TestScanRepository(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "LICENSE"), projectLicense(t))
	writeTestFile(t, filepath.Join(root, "plain.txt"), []byte("plain source text"))
	writeTestFile(t, filepath.Join(root, "binary"), []byte{'t', 'e', 'x', 't', 0, 'd', 'a', 't', 'a'})
	writeTestFile(t, filepath.Join(root, "large.txt"), []byte(strings.Repeat("x", 2_000)))
	writeTestFile(t, filepath.Join(root, "vendor", "LICENSE"), projectLicense(t))
	writeTestFile(t, filepath.Join(root, ".hidden", "LICENSE"), projectLicense(t))
	writeTestFile(t, filepath.Join(root, "ignored", "LICENSE"), projectLicense(t))
	writeTestFile(
		t,
		filepath.Join(root, "deep", "second", "third", "LICENSE"),
		projectLicense(t),
	)

	report, err := scanRepository(
		context.Background(),
		newTestMatcher(t),
		root,
		scanOptions{
			MaxDepth:    2,
			MaxFileSize: 1_500,
			Workers:     2,
			SkipDirs:    map[string]bool{"ignored": true},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if report.Summary.FilesVisited != 4 {
		t.Errorf("files visited = %d, want 4", report.Summary.FilesVisited)
	}
	if report.Summary.FilesScanned != 2 {
		t.Errorf("files scanned = %d, want 2", report.Summary.FilesScanned)
	}
	if report.Summary.FilesSkippedBinary != 1 {
		t.Errorf("binary files skipped = %d, want 1", report.Summary.FilesSkippedBinary)
	}
	if report.Summary.FilesSkippedSize != 1 {
		t.Errorf("large files skipped = %d, want 1", report.Summary.FilesSkippedSize)
	}
	if report.Summary.DirectoriesSkipped != 4 {
		t.Errorf("directories skipped = %d, want 4", report.Summary.DirectoriesSkipped)
	}
	if report.Summary.ErrorCount != 0 {
		t.Errorf("errors = %d, want 0", report.Summary.ErrorCount)
	}
	if len(report.Skipped) != 6 {
		t.Errorf("skipped = %#v, want 6 records", report.Skipped)
	}
	for _, want := range []skipRecord{
		{Path: ".hidden", Reason: "hidden-directory"},
		{Path: "binary", Reason: "binary"},
		{Path: "deep/second/third", Reason: "depth"},
		{Path: "ignored", Reason: "configured-directory"},
		{Path: "large.txt", Reason: "size"},
		{Path: "vendor", Reason: "project-scope"},
	} {
		if !hasSkip(report.Skipped, want) {
			t.Errorf("skipped = %#v, want %#v", report.Skipped, want)
		}
	}
	if len(report.Files) != 1 || report.Files[0].Path != "LICENSE" {
		t.Fatalf("files = %#v, want LICENSE only", report.Files)
	}
	if report.Files[0].Encoding != "utf-8" {
		t.Errorf("encoding = %q, want utf-8", report.Files[0].Encoding)
	}
	if !hasMITExpression(report.Files[0]) {
		t.Errorf("LICENSE detections = %#v, want mit", report.Files[0].Detections)
	}
	if !hasExpressionRecord(report.Expressions, "MIT") {
		t.Errorf("expressions = %#v, want mit", report.Expressions)
	}
	if report.Corpus.RuleCount == 0 || report.Corpus.SourceCommit == "" {
		t.Errorf("corpus = %#v, want populated metadata", report.Corpus)
	}
}

func TestScanRepositorySkipsDetectedBinary(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	files := map[string][]byte{
		"control":   []byte("plain\x01text"),
		"early-nul": []byte("plain\x00text"),
		"late-nul":  append([]byte(strings.Repeat("x", classificationProbeSize)), 0),
		"pdf":       []byte("%PDF-1.7\n%%EOF\n"),
	}
	for name, data := range files {
		writeTestFile(t, filepath.Join(root, name), data)
	}

	report, err := scanRepository(
		context.Background(),
		newTestMatcher(t),
		root,
		defaultTestScanOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.FilesSkippedBinary != len(files) {
		t.Errorf(
			"binary files skipped = %d, want %d",
			report.Summary.FilesSkippedBinary,
			len(files),
		)
	}
	if report.Summary.FilesScanned != 0 {
		t.Errorf("files scanned = %d, want 0", report.Summary.FilesScanned)
	}
	for name := range files {
		if !hasSkip(report.Skipped, skipRecord{Path: name, Reason: skipReasonBinary}) {
			t.Errorf("skipped = %#v, want %s as binary", report.Skipped, name)
		}
	}
}

func TestIdentificationRecordsAndSummary(t *testing.T) {
	t.Parallel()

	file := fileRecord{Detections: []detectionRecord{
		{
			Expression:     "MIT",
			Identification: licenses.Identified,
			Matches:        []matchRecord{{RuleID: "mit.RULE"}},
		},
		{
			Expression:     "MIT AND LicenseRef-scancode-free-unknown",
			Identification: licenses.Partial,
			Matches:        []matchRecord{{RuleID: "partial.RULE"}},
		},
		{
			Expression:     "LicenseRef-scancode-unknown-license-reference",
			Identification: licenses.NoAssertion,
			Matches:        []matchRecord{{RuleID: "unknown.RULE"}},
		},
	}}
	var summary scanSummary
	addIdentificationSummary(&summary, file.Detections)
	if summary.FilesWithIdentifiedDetections != 1 ||
		summary.FilesWithPartialDetections != 1 ||
		summary.FilesWithNoAssertionDetections != 1 {
		t.Errorf("summary = %#v, want one file in each state", summary)
	}

	expressions := make(map[string]*expressionRecord)
	addExpressionRecords(expressions, file)
	for _, detection := range file.Detections {
		record := expressions[detection.Expression]
		if record == nil || record.Identification != detection.Identification {
			t.Errorf(
				"expression %q = %#v, want %q",
				detection.Expression,
				record,
				detection.Identification,
			)
		}
	}
}

func TestScanRepositoryFollowsExplicitRootSymlinks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	directory := filepath.Join(root, "repository")
	writeTestFile(t, filepath.Join(directory, "LICENSE"), projectLicense(t))
	directoryLink := filepath.Join(root, "repository-link")
	if err := os.Symlink(directory, directoryLink); err != nil {
		t.Skipf("create directory symlink: %v", err)
	}
	fileLink := filepath.Join(root, "license-link")
	if err := os.Symlink(filepath.Join(directory, "LICENSE"), fileLink); err != nil {
		t.Skipf("create file symlink: %v", err)
	}

	options := defaultTestScanOptions()
	for _, path := range []string{directoryLink, fileLink} {
		report, err := scanRepository(context.Background(), newTestMatcher(t), path, options)
		if err != nil {
			t.Fatalf("scan %s: %v", path, err)
		}
		if report.Summary.FilesScanned != 1 {
			t.Errorf("scan %s: files scanned = %d, want 1", path, report.Summary.FilesScanned)
		}
		if len(report.Files) != 1 || !hasMITExpression(report.Files[0]) {
			t.Errorf("scan %s: files = %#v, want MIT detection", path, report.Files)
		}
	}
}

func TestScanRepositorySkipsTreeSymlinks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "LICENSE")
	writeTestFile(t, outside, projectLicense(t))
	link := filepath.Join(root, "linked-license")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("create symlink: %v", err)
	}

	report, err := scanRepository(
		context.Background(),
		newTestMatcher(t),
		root,
		defaultTestScanOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.FilesScanned != 0 {
		t.Errorf("files scanned = %d, want 0", report.Summary.FilesScanned)
	}
	if !hasSkip(report.Skipped, skipRecord{Path: "linked-license", Reason: "symlink"}) {
		t.Errorf("skipped = %#v, want linked-license symlink", report.Skipped)
	}
}

func TestScanRepositoryAllScopeIncludesDefaultSkips(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "vendor", "LICENSE"), projectLicense(t))
	writeTestFile(t, filepath.Join(root, ".hidden", "LICENSE"), projectLicense(t))
	writeTestFile(t, filepath.Join(root, ".git", "LICENSE"), projectLicense(t))
	options := defaultTestScanOptions()
	options.NoDefaultSkip = true

	report, err := scanRepository(context.Background(), newTestMatcher(t), root, options)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.FilesScanned != 2 {
		t.Errorf("files scanned = %d, want 2", report.Summary.FilesScanned)
	}
	if len(report.Skipped) != 1 || !hasSkip(report.Skipped, skipRecord{
		Path:   ".git",
		Reason: skipReasonVersionControl,
	}) {
		t.Errorf("skipped = %#v, want .git only", report.Skipped)
	}
	for _, path := range []string{".hidden/LICENSE", "vendor/LICENSE"} {
		if !hasFile(report.Files, path) {
			t.Errorf("files = %#v, want %s", report.Files, path)
		}
	}
	foundMIT := false
	for _, record := range report.Expressions {
		if record.Expression == "MIT" && record.Files != 2 {
			t.Errorf("MIT files = %d, want 2", record.Files)
		}
		if record.Expression == "MIT" {
			foundMIT = true
		}
	}
	if !foundMIT {
		t.Errorf("expressions = %#v, want mit", report.Expressions)
	}
}

func TestScanRepositoryDecodesLicenseText(t *testing.T) {
	t.Parallel()

	prefix := []byte("// cafés distribués\n\n")
	license := projectLicense(t)
	plain := make([]byte, 0, len(prefix)+len(license))
	plain = append(plain, prefix...)
	plain = append(plain, license...)
	matcher, err := licenses.New(licenses.WithMatchedText())
	if err != nil {
		t.Fatal(err)
	}
	plainResult, err := matcher.Match(context.Background(), plain)
	if err != nil {
		t.Fatal(err)
	}
	plainMatch, ok := findRuleMatch(plainResult, "mit.LICENSE")
	if !ok {
		t.Fatal("plain license has no mit.LICENSE match")
	}

	tests := []struct {
		name     string
		data     []byte
		encoding string
		start    int
		end      int
	}{
		{
			name:     "UTF-8",
			data:     plain,
			encoding: "utf-8",
			start:    plainMatch.Start,
			end:      plainMatch.End,
		},
		{
			name:     "UTF-8 BOM",
			data:     append([]byte{0xef, 0xbb, 0xbf}, plain...),
			encoding: "utf-8",
			start:    utf8BOMSize + plainMatch.Start,
			end:      utf8BOMSize + plainMatch.End,
		},
		{
			name:     "UTF-16LE",
			data:     encodeUTF16(plain, binary.LittleEndian),
			encoding: "utf-16le",
			start:    utf16RawOffset(plain, plainMatch.Start),
			end:      utf16RawOffset(plain, plainMatch.End),
		},
		{
			name:     "UTF-16BE",
			data:     encodeUTF16(plain, binary.BigEndian),
			encoding: "utf-16be",
			start:    utf16RawOffset(plain, plainMatch.Start),
			end:      utf16RawOffset(plain, plainMatch.End),
		},
		{
			name:     "Latin-1",
			data:     encodeLatin1(plain),
			encoding: "iso-8859-1",
			start:    len(encodeLatin1(plain[:plainMatch.Start])),
			end:      len(encodeLatin1(plain[:plainMatch.End])),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "LICENSE")
			writeTestFile(t, path, test.data)
			report, err := scanRepository(
				context.Background(),
				matcher,
				path,
				defaultTestScanOptions(),
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Files) != 1 {
				t.Fatalf("files = %#v, want one", report.Files)
			}
			file := report.Files[0]
			if file.Encoding != test.encoding {
				t.Errorf("encoding = %q, want %q", file.Encoding, test.encoding)
			}
			match, ok := findRecordMatch(file, "mit.LICENSE")
			if !ok {
				t.Fatalf("detections = %#v, want mit.LICENSE", file.Detections)
			}
			if match.Start != test.start || match.End != test.end {
				t.Errorf(
					"range = %d:%d, want %d:%d",
					match.Start,
					match.End,
					test.start,
					test.end,
				)
			}
			if match.Matched != string(plainMatch.Matched) {
				t.Errorf("matched text differs from decoded UTF-8 reference")
			}
		})
	}
}

func TestScanRepositoryFallsBackToLatin1ForMalformedUTF16(t *testing.T) {
	t.Parallel()

	license := projectLicense(t)
	prefix := []byte{0xff, 0xfe, 'x'}
	if (len(prefix)-2+len(license))%2 == 0 {
		prefix = append(prefix, 'x')
	}
	data := make([]byte, 0, len(prefix)+len(license))
	data = append(data, prefix...)
	data = append(data, license...)
	path := filepath.Join(t.TempDir(), "LICENSE")
	writeTestFile(t, path, data)

	report, err := scanRepository(
		context.Background(),
		newTestMatcher(t),
		path,
		defaultTestScanOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Files) != 1 {
		t.Fatalf("files = %#v, want one", report.Files)
	}
	if report.Files[0].Encoding != "iso-8859-1" {
		t.Errorf("encoding = %q, want iso-8859-1", report.Files[0].Encoding)
	}
	if !hasMITExpression(report.Files[0]) {
		t.Errorf("detections = %#v, want MIT", report.Files[0].Detections)
	}
}

func TestScanRepositoryDemotesReferenceAcrossMarkdownBlocks(t *testing.T) {
	t.Parallel()

	// From sigstore-ruby: the repository name at the end of a URL became
	// adjacent to the following License heading after tokenization.
	root := t.TempDir()
	readme := []byte(
		"Bug reports and pull requests are welcome at " +
			"<https://github.com/sigstore/sigstore-ruby>.\n\n" +
			"## License\n\n" +
			"The gem is available under the terms of the " +
			"[Apache 2](https://opensource.org/licenses/Apache-2.0).\n",
	)
	writeTestFile(t, filepath.Join(root, "README.md"), readme)
	matcher, err := licenses.New(licenses.WithMatchedText())
	if err != nil {
		t.Fatal(err)
	}

	report, err := scanRepository(
		context.Background(),
		matcher,
		root,
		defaultTestScanOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if hasExpressionRecord(report.Expressions, "Ruby") {
		t.Errorf("expressions = %#v, do not want ruby", report.Expressions)
	}
	if !hasExpressionRecord(report.Expressions, "Apache-2.0") {
		t.Errorf("expressions = %#v, want apache-2.0", report.Expressions)
	}
	if len(report.Files) != 1 {
		t.Fatalf("files = %#v, want README.md", report.Files)
	}
	clue, ok := findRubyReferenceClue(report.Files[0])
	if !ok {
		t.Fatalf("clues = %#v, want ruby_15.RULE", report.Files[0].Clues)
	}
	if clue.Kind != licenses.KindReference || clue.Score != 80 {
		t.Errorf("ruby clue = %#v, want relevance-80 reference", clue)
	}
	if got := string(readme[clue.Start:clue.End]); got != "ruby>.\n\n## License" {
		t.Errorf("matched bytes = %q", got)
	}
	if clue.Matched != "ruby>.\n\n## License" {
		t.Errorf("clue matched text = %q", clue.Matched)
	}
}

func TestScanRepositoryDemotesReferenceAcrossMarkdownTableRows(t *testing.T) {
	t.Parallel()

	// From an untracked rails/webpacker report. ScanCode 32 combined the Ruby
	// language cell and MIT license cell into ruby AND mit; the project is MIT.
	root := t.TempDir()
	readme := []byte(
		"| Languages | JavaScript, Ruby |\n" +
			"| License | mit |\n",
	)
	writeTestFile(t, filepath.Join(root, "report.md"), readme)

	report, err := scanRepository(
		context.Background(),
		newTestMatcher(t),
		root,
		defaultTestScanOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if hasExpressionRecord(report.Expressions, "Ruby") {
		t.Errorf("expressions = %#v, do not want ruby", report.Expressions)
	}
	if !hasExpressionRecord(report.Expressions, "MIT") {
		t.Errorf("expressions = %#v, want mit", report.Expressions)
	}
	if len(report.Files) != 1 {
		t.Fatalf("files = %#v, want report.md", report.Files)
	}
	clue, ok := findRubyReferenceClue(report.Files[0])
	if !ok {
		t.Fatalf("clues = %#v, want ruby_15.RULE", report.Files[0].Clues)
	}
	if got := string(readme[clue.Start:clue.End]); got != "Ruby |\n| License" {
		t.Errorf("matched bytes = %q", got)
	}
}

func TestScanRepositoryKeepsRubyAlternativeWithinMarkdownBlock(t *testing.T) {
	t.Parallel()

	// ruby2_keywords 0.0.5 declares Ruby OR BSD-2-Clause here while its LICENSE
	// contains BSD alone. ScanCode 32 kept Ruby but also added a spurious MIT.
	root := t.TempDir()
	readme := []byte(
		"## License\n\n" +
			"The gem is available as open source under the terms of the\n" +
			"[Ruby License] or the [2-Clause BSD License].\n\n" +
			"[Ruby License]: https://www.ruby-lang.org/en/about/license.txt\n" +
			"[2-Clause BSD License]: https://opensource.org/licenses/BSD-2-Clause\n",
	)
	writeTestFile(t, filepath.Join(root, "README.md"), readme)

	report, err := scanRepository(
		context.Background(),
		newTestMatcher(t),
		root,
		defaultTestScanOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{"Ruby", "BSD-2-Clause"} {
		if !hasExpressionRecord(report.Expressions, expression) {
			t.Errorf("expressions = %#v, want %s", report.Expressions, expression)
		}
	}
	if hasExpressionRecord(report.Expressions, "MIT") {
		t.Errorf("expressions = %#v, do not want mit", report.Expressions)
	}
	if len(report.Files) != 1 {
		t.Fatalf("files = %#v, want README.md", report.Files)
	}
	if _, ok := findRubyReferenceClue(report.Files[0]); ok {
		t.Errorf("clues = %#v, do not want ruby_15.RULE", report.Files[0].Clues)
	}
}

func TestApplyScanPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		path       string
		text       string
		kind       licenses.Kind
		score      float64
		detections int
		clues      int
	}{
		{
			name:  "blank line",
			path:  "README.md",
			text:  "Ruby\n\nLicense",
			clues: 1,
		},
		{
			name:  "heading",
			path:  "README.md",
			text:  "Ruby\n## License",
			clues: 1,
		},
		{
			name:  "table",
			path:  "README.md",
			text:  "| Ruby |\n| License |",
			clues: 1,
		},
		{
			name:  "unordered list",
			path:  "README.md",
			text:  "Ruby\n- License",
			clues: 1,
		},
		{
			name:  "asterisk list",
			path:  "README.md",
			text:  "* Ruby\n* License",
			clues: 1,
		},
		{
			name:  "plus list",
			path:  "README.md",
			text:  "Ruby\n+ License",
			clues: 1,
		},
		{
			name:  "blockquote",
			path:  "README.md",
			text:  "Ruby\n> License",
			clues: 1,
		},
		{
			name:  "ordered list",
			path:  "README.md",
			text:  "Ruby\n1. License",
			clues: 1,
		},
		{
			name:  "parenthesized ordered list",
			path:  "README.md",
			text:  "Ruby\n1) License",
			clues: 1,
		},
		{
			name:       "soft line wrap",
			path:       "README.md",
			text:       "Ruby\nLicense",
			detections: 1,
		},
		{
			name:       "CRLF soft line wrap",
			path:       "README.md",
			text:       "Ruby\r\nLicense",
			detections: 1,
		},
		{
			name:       "trailing whitespace soft line wrap",
			path:       "README.md",
			text:       "Ruby  \nLicense",
			detections: 1,
		},
		{
			name:       "hash comment soft line wrap",
			path:       "setup.py",
			text:       "# Ruby\n# License",
			detections: 1,
		},
		{
			name:       "block comment soft line wrap",
			path:       "source.c",
			text:       " * Ruby\n * License",
			detections: 1,
		},
		{
			name:  "comment paragraph break",
			path:  "setup.py",
			text:  "# Ruby\n#\n# License",
			clues: 1,
		},
		{
			name:       "legal file",
			path:       "LICENSE.md",
			text:       "Ruby\n\nLicense",
			detections: 1,
		},
		{
			name:       "licenses directory",
			path:       "LICENSES/component.txt",
			text:       "Ruby\n\nLicense",
			detections: 1,
		},
		{
			name:       "similarly named source file",
			path:       "src/licenses.go",
			text:       "Ruby\nLicense",
			detections: 1,
		},
		{
			name:  "full relevance still crosses blocks",
			path:  "README.md",
			text:  "Ruby\n\nLicense",
			score: 100,
			clues: 1,
		},
		{
			name:       "notice rule",
			path:       "README.md",
			text:       "Ruby\n\nLicense",
			kind:       licenses.KindNotice,
			detections: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kind := test.kind
			if kind == "" {
				kind = licenses.KindReference
			}
			score := test.score
			if score == 0 {
				score = 80
			}
			start := strings.Index(test.text, "Ruby")
			end := strings.LastIndex(test.text, "License") + len("License")
			result := licenses.Result{Detections: []licenses.Detection{{
				Expression: "Ruby",
				Matches: []licenses.Match{{
					RuleID:  "ruby.RULE",
					Kind:    kind,
					Score:   score,
					Start:   start,
					End:     end,
					Matched: []byte(test.text[start:end]),
				}},
			}}}
			applyScanPolicy(test.path, []byte(test.text), &result)
			if len(result.Detections) != test.detections || len(result.Clues) != test.clues {
				t.Errorf(
					"detections, clues = %d, %d; want %d, %d",
					len(result.Detections),
					len(result.Clues),
					test.detections,
					test.clues,
				)
			}
			if test.clues == 1 &&
				string(result.Clues[0].Matched) != test.text[start:end] {
				t.Errorf("clue matched text = %q", result.Clues[0].Matched)
			}
		})
	}
}

func TestScanRepositoryKeepsReferenceInExplicitLicensesFile(t *testing.T) {
	t.Parallel()

	filePath := filepath.Join(t.TempDir(), "LICENSES", "component.txt")
	writeTestFile(t, filePath, []byte("Ruby\n\nLicense\n"))

	report, err := scanRepository(
		context.Background(),
		newTestMatcher(t),
		filePath,
		defaultTestScanOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !hasExpressionRecord(report.Expressions, "Ruby") {
		t.Errorf("expressions = %#v, want ruby", report.Expressions)
	}
	if len(report.Files) != 1 || report.Files[0].Path != "component.txt" {
		t.Fatalf("files = %#v, want component.txt", report.Files)
	}
	if _, ok := findRubyReferenceClue(report.Files[0]); ok {
		t.Errorf("clues = %#v, do not want ruby_15.RULE", report.Files[0].Clues)
	}
}

func TestScanPolicyUsesDecodedText(t *testing.T) {
	t.Parallel()

	text := []byte(
		"café\n" +
			"https://github.com/sigstore/sigstore-ruby>.\n\n" +
			"## License\n",
	)
	tests := []struct {
		name string
		data []byte
	}{
		{name: "UTF-16LE", data: encodeUTF16(text, binary.LittleEndian)},
		{name: "Latin-1", data: encodeLatin1(text)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filePath := filepath.Join(t.TempDir(), "README.md")
			writeTestFile(t, filePath, test.data)

			report, err := scanRepository(
				context.Background(),
				newTestMatcher(t),
				filePath,
				defaultTestScanOptions(),
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Files) != 1 {
				t.Fatalf("files = %#v, want README.md", report.Files)
			}
			if _, ok := findRubyReferenceClue(report.Files[0]); !ok {
				t.Fatalf("clues = %#v, want ruby_15.RULE", report.Files[0].Clues)
			}
		})
	}
}

func TestLegalFileNames(t *testing.T) {
	t.Parallel()

	for _, filePath := range []string{
		"license/component.txt",
		"licenses/component.txt",
		"licence/component.txt",
		"licences/component.txt",
		"LICENSES",
		"LICENSES.txt",
		"LICENCES",
		"NOTICES",
		"NOTICES.txt",
	} {
		if !isLegalFile(filePath) {
			t.Errorf("isLegalFile(%q) = false, want true", filePath)
		}
	}
}

func TestScanFileEnforcesSizeAfterDiscovery(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "growing.txt")
	writeTestFile(t, path, []byte("small"))
	options := defaultTestScanOptions()
	options.MaxFileSize = 10
	var summary scanSummary
	tasks, _, _, err := discoverFiles(
		context.Background(),
		path,
		options,
		&summary,
	)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, []byte(strings.Repeat("large", 10)))
	outcome := scanFile(context.Background(), nil, tasks[0], options.MaxFileSize)
	if !outcome.tooLarge {
		t.Fatalf("outcome = %#v, want tooLarge", outcome)
	}
}

func TestScanRepositoryRecordsUnreadableFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Unix file modes required")
	}

	root := t.TempDir()
	path := filepath.Join(root, "unreadable")
	writeTestFile(t, path, []byte("text"))
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	report, err := scanRepository(
		context.Background(),
		newTestMatcher(t),
		root,
		defaultTestScanOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Errors) == 0 {
		t.Skip("current user can read mode-000 files")
	}
	if report.Errors[0].Path != "unreadable" {
		t.Errorf("errors = %#v, want unreadable path", report.Errors)
	}
}

func TestScanRepositoryRecordsCandidateLimit(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "repeated.txt")
	writeTestFile(t, path, []byte(strings.Repeat("mit license ", 100_000)))
	options := defaultTestScanOptions()
	options.MaxFileSize = 2 << 20

	report, err := scanRepository(
		context.Background(),
		newTestMatcher(t),
		path,
		options,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Errors) != 1 {
		t.Fatalf("errors = %#v, want one", report.Errors)
	}
	if !strings.Contains(report.Errors[0].Error, licenses.ErrTooManyMatches.Error()) {
		t.Errorf("error = %q, want candidate limit", report.Errors[0].Error)
	}
	if reportExitCode(report) != exitScanErrors {
		t.Errorf("exit code = %d, want %d", reportExitCode(report), exitScanErrors)
	}
}

func TestEffectiveWorkerCount(t *testing.T) {
	t.Parallel()

	if got := effectiveWorkerCount(5_000, 5_000); got != maxWorkers {
		t.Errorf("worker count = %d, want %d", got, maxWorkers)
	}
	if got := effectiveWorkerCount(8, 3); got != 3 {
		t.Errorf("worker count = %d, want 3", got)
	}
}

func TestDiscoverFilesStopsAtLimit(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		writeTestFile(t, filepath.Join(root, name), []byte(name))
	}
	options := scanOptions{
		MaxFiles:    2,
		MaxFileSize: defaultMaxFileSize,
		Workers:     1,
	}
	var summary scanSummary
	tasks, scanErrors, skipped, err := discoverFiles(
		context.Background(),
		root,
		options,
		&summary,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(scanErrors) != 0 {
		t.Fatalf("errors = %#v, want none", scanErrors)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %#v, want none", skipped)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want 2", len(tasks))
	}
	if !summary.Truncated {
		t.Fatal("truncated = false, want true")
	}
	if summary.FilesVisited != 2 {
		t.Errorf("files visited = %d, want 2", summary.FilesVisited)
	}
}

func TestDiscoverExplicitFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "NOTICE")
	writeTestFile(t, path, []byte("notice"))
	options := scanOptions{
		MaxDepth:    1,
		MaxFiles:    1,
		MaxFileSize: 100,
		Workers:     1,
	}
	var summary scanSummary
	tasks, scanErrors, skipped, err := discoverFiles(
		context.Background(),
		path,
		options,
		&summary,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(scanErrors) != 0 {
		t.Fatalf("errors = %#v, want none", scanErrors)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %#v, want none", skipped)
	}
	if len(tasks) != 1 || tasks[0].display != "NOTICE" {
		t.Fatalf("tasks = %#v, want NOTICE", tasks)
	}
}

func TestScanRepositoryCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	options := scanOptions{
		MaxFileSize: defaultMaxFileSize,
		Workers:     1,
	}
	_, err := scanRepository(ctx, newTestMatcher(t), "../../LICENSE", options)
	if err != context.Canceled {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestScanHelpers(t *testing.T) {
	t.Parallel()

	if pathDepth(filepath.Join("one", "two", "three")) != 3 {
		t.Errorf("path depth = %d, want 3", pathDepth(filepath.Join("one", "two", "three")))
	}
	if skippedDirectoryReason("vendor", scanOptions{}) != "project-scope" {
		t.Error("vendor directory was not skipped in project scope")
	}
	if skippedDirectoryReason(".git", scanOptions{}) != skipReasonVersionControl {
		t.Error(".git directory was not skipped")
	}
	if skippedDirectoryReason(
		"generated",
		scanOptions{SkipDirs: map[string]bool{"generated": true}},
	) != "configured-directory" {
		t.Error("extra directory was not skipped")
	}
	if skippedDirectoryReason("vendor", scanOptions{NoDefaultSkip: true}) != "" {
		t.Error("vendor directory was skipped in all scope")
	}
	if skippedDirectoryReason(".git", scanOptions{NoDefaultSkip: true}) !=
		skipReasonVersionControl {
		t.Error(".git directory was not skipped in all scope")
	}
	if skippedDirectoryReason("src", scanOptions{}) != "" {
		t.Error("src directory was skipped")
	}
}

func TestReadScannableFileClassification(t *testing.T) {
	t.Parallel()

	plain := []byte("plain text")
	tests := []struct {
		name     string
		data     []byte
		maximum  int64
		kind     magic.Kind
		encoding string
		reason   magic.Reason
	}{
		{
			name: "empty",
			kind: magic.KindText,
		},
		{
			name:     "text at maximum size",
			data:     plain,
			maximum:  int64(len(plain)),
			kind:     magic.KindText,
			encoding: "utf-8",
		},
		{
			name:     "UTF-16LE",
			data:     encodeUTF16(plain, binary.LittleEndian),
			kind:     magic.KindText,
			encoding: "utf-16le",
		},
		{
			name:     "UTF-16BE",
			data:     encodeUTF16(plain, binary.BigEndian),
			kind:     magic.KindText,
			encoding: "utf-16be",
		},
		{
			name:   "malformed UTF-16",
			data:   []byte{0xff, 0xfe, 'x'},
			kind:   magic.KindUnknown,
			reason: magic.ReasonInvalidText,
		},
		{
			name:   "invalid UTF-8",
			data:   []byte{'c', 'a', 'f', 0xe9},
			kind:   magic.KindUnknown,
			reason: magic.ReasonInvalidText,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input")
			writeTestFile(t, path, test.data)

			data, detection, tooLarge, err := readScannableFile(path, test.maximum)
			if err != nil {
				t.Fatal(err)
			}
			if tooLarge {
				t.Fatal("file was reported as too large")
			}
			if string(data) != string(test.data) {
				t.Fatalf("data = %x, want %x", data, test.data)
			}
			if detection.Kind != test.kind ||
				detection.Encoding != test.encoding ||
				detection.Reason != test.reason {
				t.Errorf(
					"detection = %#v, want kind %q, encoding %q, reason %q",
					detection,
					test.kind,
					test.encoding,
					test.reason,
				)
			}
		})
	}
}

func TestValidateScanOptions(t *testing.T) {
	t.Parallel()

	tests := []scanOptions{
		{MaxDepth: -1, Workers: 1},
		{MaxFiles: -1, Workers: 1},
		{MaxFileSize: -1, Workers: 1},
		{},
	}
	for _, options := range tests {
		if err := validateScanOptions(options); err == nil {
			t.Errorf("validateScanOptions(%#v) returned nil", options)
		}
	}
}

func newTestMatcher(t *testing.T) *licenses.Matcher {
	t.Helper()
	matcher, err := licenses.New()
	if err != nil {
		t.Fatal(err)
	}
	return matcher
}

func defaultTestScanOptions() scanOptions {
	return scanOptions{
		MaxDepth:    defaultMaxDepth,
		MaxFiles:    defaultMaxFiles,
		MaxFileSize: defaultMaxFileSize,
		Workers:     2,
	}
}

func projectLicense(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func hasMITExpression(file fileRecord) bool {
	for _, detection := range file.Detections {
		if detection.Expression == "MIT" {
			return true
		}
	}
	return false
}

func hasExpressionRecord(records []expressionRecord, expression string) bool {
	for _, record := range records {
		if record.Expression == expression {
			return true
		}
	}
	return false
}

func hasSkip(records []skipRecord, want skipRecord) bool {
	for _, record := range records {
		if record == want {
			return true
		}
	}
	return false
}

func hasFile(files []fileRecord, path string) bool {
	for _, file := range files {
		if file.Path == path {
			return true
		}
	}
	return false
}

func findRuleMatch(result licenses.Result, ruleID string) (licenses.Match, bool) {
	for _, detection := range result.Detections {
		for _, match := range detection.Matches {
			if match.RuleID == ruleID {
				return match, true
			}
		}
	}
	return licenses.Match{}, false
}

func findRecordMatch(file fileRecord, ruleID string) (matchRecord, bool) {
	for _, detection := range file.Detections {
		for _, match := range detection.Matches {
			if match.RuleID == ruleID {
				return match, true
			}
		}
	}
	return matchRecord{}, false
}

func findRubyReferenceClue(file fileRecord) (matchRecord, bool) {
	for _, clue := range file.Clues {
		if clue.RuleID == "ruby_15.RULE" {
			return clue, true
		}
	}
	return matchRecord{}, false
}

func encodeUTF16(data []byte, order binary.ByteOrder) []byte {
	units := utf16.Encode([]rune(string(data)))
	encoded := make([]byte, 2+len(units)*2)
	if order == binary.LittleEndian {
		copy(encoded, []byte{0xff, 0xfe})
	} else {
		copy(encoded, []byte{0xfe, 0xff})
	}
	for index, unit := range units {
		order.PutUint16(encoded[2+index*2:], unit)
	}
	return encoded
}

func utf16RawOffset(data []byte, decodedOffset int) int {
	return 2 + len(utf16.Encode([]rune(string(data[:decodedOffset]))))*2
}

func encodeLatin1(data []byte) []byte {
	encoded := make([]byte, 0, len(data))
	for _, character := range string(data) {
		encoded = append(encoded, byte(character))
	}
	return encoded
}
