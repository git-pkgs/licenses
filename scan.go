package licenses

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/git-pkgs/magic"
	"github.com/git-pkgs/manifests"
	"github.com/git-pkgs/spdx"
)

const (
	reportSchemaVersion     = 2
	defaultMaxDepth         = 32
	defaultMaxFiles         = 10_000
	defaultMaxFileSize      = 1 << 20
	classificationProbeSize = 8 << 10
	maxWorkers              = 16
	maxReadPreallocate      = 16 << 20
	utf8BOMSize             = 3
	minimumMarkerLength     = 2
	legalRoleCount          = 2
	percentageScale         = 100
	encodingUTF8            = "utf-8"
	encodingUTF16LE         = "utf-16le"
	encodingUTF16BE         = "utf-16be"
	encodingLatin1          = "iso-8859-1"
	scannerName             = "git-pkgs/licenses"

	skipReasonBinary              = "binary"
	skipReasonConfiguredDirectory = "configured-directory"
	skipReasonDepth               = "depth"
	skipReasonHiddenDirectory     = "hidden-directory"
	skipReasonNonRegular          = "non-regular"
	skipReasonProjectScope        = "project-scope"
	skipReasonSize                = "size"
	skipReasonSymlink             = "symlink"
	skipReasonVersionControl      = "version-control"
	scopeAll                      = "all"
	scopeProject                  = "project"
)

var errFileLimit = errors.New("file limit reached")

var defaultSkippedDirectories = map[string]bool{
	"vendor":       true,
	"node_modules": true,
	"__pycache__":  true,
	".bundle":      true,
	".venv":        true,
	"venv":         true,
	"target":       true,
	"build":        true,
	"dist":         true,
	"out":          true,
	"_build":       true,
	"deps":         true,
	"Pods":         true,
	"third_party":  true,
	"thirdparty":   true,
	"external":     true,
	"testdata":     true,
	"tmp":          true,
	"temp":         true,
	"cache":        true,
	"coverage":     true,
}

const (
	// ReportSchemaVersion is the additive JSON report schema version.
	ReportSchemaVersion = reportSchemaVersion
	// DefaultMaxDepth is the default maximum directory depth.
	DefaultMaxDepth = defaultMaxDepth
	// DefaultMaxFiles is the default maximum number of visited files.
	DefaultMaxFiles = defaultMaxFiles
	// DefaultMaxFileSize is the default maximum number of bytes per file.
	DefaultMaxFileSize = defaultMaxFileSize
	// ScannerName identifies this scanner in reports.
	ScannerName = scannerName
	// ScopeAll includes dependency, build, cache, and test-data directories.
	ScopeAll = scopeAll
	// ScopeProject skips dependency, build, cache, and test-data directories.
	ScopeProject = scopeProject
)

// DefaultScanOptions returns the default traversal limits and worker count.
func DefaultScanOptions() ScanOptions {
	return ScanOptions{
		MaxDepth:    defaultMaxDepth,
		MaxFiles:    defaultMaxFiles,
		MaxFileSize: defaultMaxFileSize,
		Workers:     defaultWorkerCount(),
	}
}

// ValidateScanOptions reports invalid limits or worker counts.
func ValidateScanOptions(options ScanOptions) error {
	return validateScanOptions(options)
}

// ScanRepository scans a file or directory with matcher.
func ScanRepository(
	ctx context.Context,
	matcher *Matcher,
	root string,
	options ScanOptions,
) (ScanReport, error) {
	return scanRepository(ctx, matcher, root, options, options.ScannerVersion)
}

// ScanOptions controls repository traversal and file scanning.
type ScanOptions struct {
	MaxDepth          int             // Maximum directory depth; zero disables the limit.
	MaxFiles          int             // Maximum number of visited files; zero disables the limit.
	MaxFileSize       int64           // Maximum bytes read per file; zero disables the limit.
	Workers           int             // Requested concurrent file scans, capped at 16.
	SkipDirs          map[string]bool // Directory base names to skip.
	NoDefaultSkip     bool            // Include hidden, dependency, build, cache, and test-data directories.
	IncludeLegalFiles bool            // Report legal files even when they contain no matches.
	ScannerVersion    string          // Scanner build version recorded in the report.
}

// ScanReport contains the deterministic result of scanning a file or directory.
type ScanReport struct {
	Schema      int                `json:"schema"`
	Root        string             `json:"root"`
	Scope       string             `json:"scope"`
	Scanner     ScannerRecord      `json:"scanner"`
	Corpus      CorpusRecord       `json:"corpus"`
	Summary     ScanSummary        `json:"summary"`
	Declared    []DeclaredRecord   `json:"declared"`
	Expressions []ExpressionRecord `json:"expressions"`
	Files       []FileRecord       `json:"files"`
	Skipped     []SkipRecord       `json:"skipped"`
	Errors      []ScanErrorRecord  `json:"errors"`
}

// ScannerRecord identifies the scanner and its build version.
type ScannerRecord struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// CorpusRecord identifies the ScanCode corpus used for a scan.
type CorpusRecord struct {
	Version      string `json:"version"`
	RuleCount    int    `json:"rule_count"`
	SourceCommit string `json:"source_commit"`
}

// ScanSummary contains file, byte, skip, and error counts for a scan.
type ScanSummary struct {
	FilesVisited                   int   `json:"files_visited"`
	FilesScanned                   int   `json:"files_scanned"`
	FilesWithDetections            int   `json:"files_with_detections"`
	FilesWithIdentifiedDetections  int   `json:"files_with_identified_detections"`
	FilesWithPartialDetections     int   `json:"files_with_partial_detections"`
	FilesWithNoAssertionDetections int   `json:"files_with_noassertion_detections"`
	FilesWithClues                 int   `json:"files_with_clues"`
	BytesScanned                   int64 `json:"bytes_scanned"`
	DirectoriesSkipped             int   `json:"directories_skipped"`
	FilesSkippedBinary             int   `json:"files_skipped_binary"`
	FilesSkippedSize               int   `json:"files_skipped_size"`
	FilesSkippedOther              int   `json:"files_skipped_other"`
	ErrorCount                     int   `json:"error_count"`
	Truncated                      bool  `json:"truncated"`
}

// ExpressionRecord summarizes a detected license expression across files.
type ExpressionRecord struct {
	Expression     string         `json:"expression"`
	Identification Identification `json:"identification"`
	Root           bool           `json:"root"`
	Files          int            `json:"files"`
	Matches        int            `json:"matches"`
}

// DeclaredRecord contains license metadata read from a package manifest.
type DeclaredRecord struct {
	Path                 string   `json:"path"`
	Raw                  []string `json:"raw"`
	LicenseFile          string   `json:"license_file"`
	NormalizedExpression string   `json:"normalized_expression"`
}

// FileRecord contains the license detections and clues reported for one file.
type FileRecord struct {
	Path                string            `json:"path"`
	Size                int64             `json:"size"`
	SHA256              string            `json:"sha256"`
	Encoding            string            `json:"encoding"`
	Text                string            `json:"text,omitempty"`
	Roles               []string          `json:"roles"`
	LicenseTextCoverage float64           `json:"license_text_coverage"`
	Detections          []DetectionRecord `json:"detections"`
	Clues               []MatchRecord     `json:"clues"`
}

// DetectionRecord groups matches that report the same license expression.
type DetectionRecord struct {
	Expression     string         `json:"expression"`
	Identification Identification `json:"identification"`
	Matches        []MatchRecord  `json:"matches"`
}

// MatchRecord describes one rule match in a scanned file.
type MatchRecord struct {
	RuleID     string   `json:"rule_id"`
	LicenseIDs []string `json:"license_ids,omitempty"`
	Kind       Kind     `json:"kind"`
	Method     Method   `json:"method"`
	Score      float64  `json:"score"`
	Coverage   float64  `json:"coverage"`
	Start      int      `json:"start"`
	End        int      `json:"end"`
	Matched    string   `json:"matched,omitempty"`
}

// ScanErrorRecord describes an error associated with one path.
type ScanErrorRecord struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// SkipRecord describes a path omitted from a scan and the reason it was omitted.
type SkipRecord struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type scanOptions = ScanOptions
type scanReport = ScanReport
type scannerRecord = ScannerRecord
type scanSummary = ScanSummary
type expressionRecord = ExpressionRecord
type declaredRecord = DeclaredRecord
type fileRecord = FileRecord
type detectionRecord = DetectionRecord
type matchRecord = MatchRecord
type scanErrorRecord = ScanErrorRecord
type skipRecord = SkipRecord

type fileTask struct {
	path       string
	display    string
	policyPath string
}

type fileOutcome struct {
	task                fileTask
	result              Result
	roles               []string
	bytes               int64
	scanned             bool
	binary              bool
	tooLarge            bool
	encoding            string
	sha256              string
	text                string
	licenseTextCoverage float64
	err                 error
}

type decodedText struct {
	data       []byte
	offsets    []int
	offsetBase int
	encoding   string
}

type fileDiscovery struct {
	ctx        context.Context
	root       string
	options    scanOptions
	summary    *scanSummary
	tasks      []fileTask
	declared   []declaredRecord
	scanErrors []scanErrorRecord
	skipped    []skipRecord
}

func defaultWorkerCount() int {
	return min(runtime.GOMAXPROCS(0), maxWorkers)
}

func scanRepository(
	ctx context.Context,
	matcher *Matcher,
	root string,
	options scanOptions,
	scannerVersion string,
) (scanReport, error) {
	if matcher == nil {
		return scanReport{}, errors.New("nil matcher")
	}
	if ctx == nil {
		return scanReport{}, errors.New("nil context")
	}
	if err := ctx.Err(); err != nil {
		return scanReport{}, err
	}
	if err := validateScanOptions(options); err != nil {
		return scanReport{}, err
	}
	corpus := matcher.Corpus()
	report := scanReport{
		Schema:   reportSchemaVersion,
		Root:     filepath.Clean(root),
		Scope:    scanScope(options),
		Declared: make([]declaredRecord, 0),
		Files:    make([]fileRecord, 0),
		Skipped:  make([]skipRecord, 0),
		Errors:   make([]scanErrorRecord, 0),
		Scanner: scannerRecord{
			Name:    scannerName,
			Version: scannerVersion,
		},
		Corpus: CorpusRecord(corpus),
	}
	discovery, err := discoverFiles(
		ctx,
		root,
		options,
		&report.Summary,
	)
	if err != nil {
		return scanReport{}, err
	}
	report.Declared = append(report.Declared, discovery.declared...)
	report.Errors = append(report.Errors, discovery.scanErrors...)
	report.Skipped = append(report.Skipped, discovery.skipped...)

	outcomes := scanFiles(ctx, matcher, discovery.tasks, options)
	expressions := make(map[string]*expressionRecord)
	for outcome := range outcomes {
		if outcome.scanned {
			report.Summary.FilesScanned++
			report.Summary.BytesScanned += outcome.bytes
		}
		switch {
		case outcome.err != nil:
			report.Errors = append(report.Errors, scanErrorRecord{
				Path:  outcome.task.display,
				Error: outcome.err.Error(),
			})
		case outcome.binary:
			report.Summary.FilesSkippedBinary++
			report.Skipped = append(report.Skipped, skipRecord{
				Path:   outcome.task.display,
				Reason: skipReasonBinary,
			})
		case outcome.tooLarge:
			report.Summary.FilesSkippedSize++
			report.Skipped = append(report.Skipped, skipRecord{
				Path:   outcome.task.display,
				Reason: skipReasonSize,
			})
		case outcome.scanned:
			if len(outcome.result.Detections) == 0 &&
				len(outcome.result.Clues) == 0 &&
				(!options.IncludeLegalFiles || len(outcome.roles) == 0) {
				continue
			}
			file := makeFileRecord(
				outcome.task.display,
				outcome.bytes,
				outcome.sha256,
				outcome.encoding,
				outcome.text,
				outcome.roles,
				outcome.licenseTextCoverage,
				outcome.result,
			)
			sortFileMatches(&file)
			if len(file.Detections) != 0 {
				report.Summary.FilesWithDetections++
				addIdentificationSummary(&report.Summary, file.Detections)
			}
			if len(file.Clues) != 0 {
				report.Summary.FilesWithClues++
			}
			report.Files = append(report.Files, file)
			addExpressionRecords(expressions, file)
		}
	}
	if err := ctx.Err(); err != nil {
		return scanReport{}, err
	}

	slices.SortFunc(report.Files, func(first, second fileRecord) int {
		return strings.Compare(first.Path, second.Path)
	})
	slices.SortFunc(report.Declared, func(first, second declaredRecord) int {
		return strings.Compare(first.Path, second.Path)
	})
	slices.SortFunc(report.Errors, func(first, second scanErrorRecord) int {
		if compared := strings.Compare(first.Path, second.Path); compared != 0 {
			return compared
		}
		return strings.Compare(first.Error, second.Error)
	})
	slices.SortFunc(report.Skipped, func(first, second skipRecord) int {
		if compared := strings.Compare(first.Path, second.Path); compared != 0 {
			return compared
		}
		return strings.Compare(first.Reason, second.Reason)
	})
	report.Expressions = make([]expressionRecord, 0, len(expressions))
	for _, expression := range expressions {
		report.Expressions = append(report.Expressions, *expression)
	}
	slices.SortFunc(report.Expressions, func(first, second expressionRecord) int {
		return strings.Compare(first.Expression, second.Expression)
	})
	report.Summary.ErrorCount = len(report.Errors)
	return report, nil
}

func scanScope(options scanOptions) string {
	if options.NoDefaultSkip {
		return scopeAll
	}
	return scopeProject
}

func validateScanOptions(options scanOptions) error {
	switch {
	case options.MaxDepth < 0:
		return errors.New("max depth must not be negative")
	case options.MaxFiles < 0:
		return errors.New("max files must not be negative")
	case options.MaxFileSize < 0:
		return errors.New("max file size must not be negative")
	case options.Workers < 1:
		return errors.New("workers must be positive")
	default:
		return nil
	}
}

func discoverFiles(
	ctx context.Context,
	root string,
	options scanOptions,
	summary *scanSummary,
) (*fileDiscovery, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	discovery := &fileDiscovery{
		ctx:     ctx,
		root:    root,
		options: options,
		summary: summary,
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		discovery.discoverExplicitFile(root, info)
		return discovery, nil
	}
	walkRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	discovery.root = walkRoot
	err = filepath.WalkDir(walkRoot, discovery.visit)
	if err != nil && !errors.Is(err, errFileLimit) {
		return nil, err
	}
	return discovery, nil
}

func (discovery *fileDiscovery) discoverExplicitFile(
	path string,
	info os.FileInfo,
) {
	discovery.summary.FilesVisited = 1
	if !info.Mode().IsRegular() {
		discovery.summary.FilesSkippedOther = 1
		discovery.skipped = append(discovery.skipped, skipRecord{
			Path:   filepath.Base(path),
			Reason: skipReasonNonRegular,
		})
		return
	}
	if discovery.options.MaxFileSize > 0 &&
		info.Size() > discovery.options.MaxFileSize {
		discovery.summary.FilesSkippedSize = 1
		discovery.skipped = append(discovery.skipped, skipRecord{
			Path:   filepath.Base(path),
			Reason: skipReasonSize,
		})
		return
	}
	display := filepath.Base(path)
	discovery.tasks = append(discovery.tasks, fileTask{
		path:       path,
		display:    display,
		policyPath: explicitPolicyPath(path),
	})
	discovery.discoverDeclaredLicense(path, display)
}

func explicitPolicyPath(filePath string) string {
	name := filepath.Base(filePath)
	directory := filepath.Base(filepath.Dir(filepath.Clean(filePath)))
	if directory == "." || directory == string(filepath.Separator) {
		return name
	}
	return filepath.ToSlash(filepath.Join(directory, name))
}

func (discovery *fileDiscovery) visit(
	path string,
	entry os.DirEntry,
	walkErr error,
) error {
	if err := discovery.ctx.Err(); err != nil {
		return err
	}
	relative := relativePath(discovery.root, path)
	if walkErr != nil {
		discovery.scanErrors = append(discovery.scanErrors, scanErrorRecord{
			Path:  relative,
			Error: walkErr.Error(),
		})
		if entry != nil && entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	if entry.IsDir() {
		return discovery.visitDirectory(relative, entry.Name())
	}
	return discovery.visitFile(path, relative, entry)
}

func (discovery *fileDiscovery) visitDirectory(relative, name string) error {
	if relative == "." {
		return nil
	}
	reason := skippedDirectoryReason(name, discovery.options)
	if discovery.options.MaxDepth > 0 &&
		pathDepth(relative) > discovery.options.MaxDepth {
		reason = skipReasonDepth
	}
	if reason != "" {
		discovery.summary.DirectoriesSkipped++
		discovery.skipped = append(discovery.skipped, skipRecord{
			Path:   relative,
			Reason: reason,
		})
		return filepath.SkipDir
	}
	return nil
}

func (discovery *fileDiscovery) visitFile(
	path string,
	relative string,
	entry os.DirEntry,
) error {
	if discovery.options.MaxFiles > 0 &&
		discovery.summary.FilesVisited >= discovery.options.MaxFiles {
		discovery.summary.Truncated = true
		return errFileLimit
	}
	discovery.summary.FilesVisited++
	if entry.Type()&os.ModeSymlink != 0 {
		discovery.summary.FilesSkippedOther++
		discovery.skipped = append(discovery.skipped, skipRecord{
			Path:   relative,
			Reason: skipReasonSymlink,
		})
		return nil
	}
	fileInfo, err := entry.Info()
	if err != nil {
		discovery.scanErrors = append(discovery.scanErrors, scanErrorRecord{
			Path:  relative,
			Error: err.Error(),
		})
		return nil
	}
	if !fileInfo.Mode().IsRegular() {
		discovery.summary.FilesSkippedOther++
		discovery.skipped = append(discovery.skipped, skipRecord{
			Path:   relative,
			Reason: skipReasonNonRegular,
		})
		return nil
	}
	if discovery.options.MaxFileSize > 0 &&
		fileInfo.Size() > discovery.options.MaxFileSize {
		discovery.summary.FilesSkippedSize++
		discovery.skipped = append(discovery.skipped, skipRecord{
			Path:   relative,
			Reason: skipReasonSize,
		})
		return nil
	}
	discovery.tasks = append(discovery.tasks, fileTask{
		path:       path,
		display:    filepath.ToSlash(relative),
		policyPath: filepath.ToSlash(relative),
	})
	discovery.discoverDeclaredLicense(path, filepath.ToSlash(relative))
	return nil
}

func (discovery *fileDiscovery) discoverDeclaredLicense(path, display string) {
	record, ok, err := declaredLicense(path, display)
	if err != nil {
		discovery.scanErrors = append(discovery.scanErrors, scanErrorRecord{
			Path:  display,
			Error: err.Error(),
		})
		return
	}
	if ok {
		discovery.declared = append(discovery.declared, record)
	}
}

func declaredLicense(path, display string) (declaredRecord, bool, error) {
	if _, kind, ok := manifests.Identify(display); !ok || kind != manifests.Manifest {
		return declaredRecord{}, false, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return declaredRecord{}, false, err
	}
	result, err := manifests.Parse(display, content)
	if err != nil {
		return declaredRecord{}, false, err
	}
	if result == nil ||
		(len(result.Licenses) == 0 && result.LicenseFile == "") {
		return declaredRecord{}, false, nil
	}
	return declaredRecord{
		Path:                 display,
		Raw:                  append([]string{}, result.Licenses...),
		LicenseFile:          result.LicenseFile,
		NormalizedExpression: normalizeDeclaredExpression(result.Licenses),
	}, true, nil
}

func normalizeDeclaredExpression(raw []string) string {
	if len(raw) == 0 {
		return ""
	}
	normalized, err := spdx.NormalizeExpressionLax(strings.Join(raw, " OR "))
	if err != nil {
		return ""
	}
	return normalized
}

func skippedDirectoryReason(name string, options scanOptions) string {
	if name == ".git" {
		return skipReasonVersionControl
	}
	if options.SkipDirs[name] {
		return skipReasonConfiguredDirectory
	}
	if options.NoDefaultSkip {
		return ""
	}
	if strings.HasPrefix(name, ".") {
		return skipReasonHiddenDirectory
	}
	if defaultSkippedDirectories[name] {
		return skipReasonProjectScope
	}
	return ""
}

func relativePath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(relative)
}

func pathDepth(path string) int {
	if path == "." || path == "" {
		return 0
	}
	return strings.Count(filepath.ToSlash(filepath.Clean(path)), "/") + 1
}

func scanFiles(
	ctx context.Context,
	matcher *Matcher,
	tasks []fileTask,
	options scanOptions,
) <-chan fileOutcome {
	outcomes := make(chan fileOutcome)
	if len(tasks) == 0 {
		close(outcomes)
		return outcomes
	}
	jobs := make(chan fileTask, len(tasks))
	for _, task := range tasks {
		jobs <- task
	}
	close(jobs)

	var workers sync.WaitGroup
	workerCount := effectiveWorkerCount(options.Workers, len(tasks))
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for task := range jobs {
				outcome := scanFile(ctx, matcher, task, options)
				select {
				case outcomes <- outcome:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		workers.Wait()
		close(outcomes)
	}()
	return outcomes
}

func effectiveWorkerCount(requested, taskCount int) int {
	return min(requested, maxWorkers, taskCount)
}

func scanFile(
	ctx context.Context,
	matcher *Matcher,
	task fileTask,
	options scanOptions,
) fileOutcome {
	data, detection, tooLarge, err := readScannableFile(task.path, options.MaxFileSize)
	if err != nil {
		return fileOutcome{task: task, err: err}
	}
	if tooLarge {
		return fileOutcome{task: task, tooLarge: true}
	}
	if detection.Kind == magic.KindBinary {
		return fileOutcome{task: task, binary: true}
	}
	decoded := decodeText(data, detection)
	result, err := matcher.Match(ctx, decoded.data)
	if err != nil {
		return fileOutcome{
			task:     task,
			bytes:    int64(len(data)),
			scanned:  true,
			encoding: decoded.encoding,
			err:      err,
		}
	}
	applyScanPolicy(task.policyPath, decoded.data, &result)
	licenseTextCoverage := calculateLicenseTextCoverage(result, len(decoded.data))
	remapResultOffsets(&result, decoded)
	roles := LegalFileRoles(task.policyPath)
	checksum := ""
	if len(result.Detections) != 0 || len(result.Clues) != 0 ||
		(options.IncludeLegalFiles && len(roles) != 0) {
		digest := sha256.Sum256(data)
		checksum = hex.EncodeToString(digest[:])
	}
	text := ""
	if options.IncludeLegalFiles && len(roles) != 0 {
		text = string(decoded.data)
	}
	return fileOutcome{
		task:                task,
		result:              result,
		roles:               roles,
		bytes:               int64(len(data)),
		scanned:             true,
		encoding:            decoded.encoding,
		sha256:              checksum,
		text:                text,
		licenseTextCoverage: licenseTextCoverage,
	}
}

func readScannableFile(path string, maximum int64) ([]byte, magic.Result, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, magic.Result{}, false, err
	}
	defer func() { _ = file.Close() }()

	var data bytes.Buffer
	probeLimit := int64(classificationProbeSize)
	if maximum > 0 {
		probeLimit = min(probeLimit, maximum+1)
	}
	_, err = io.CopyN(&data, file, probeLimit)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, magic.Result{}, false, err
	}
	if detection := magic.DetectPrefix(data.Bytes()); detection.Kind == magic.KindBinary {
		return nil, detection, false, nil
	}
	if maximum > 0 && int64(data.Len()) > maximum {
		return nil, magic.Result{}, true, nil
	}
	growReadBuffer(file, &data, maximum)
	reader := io.Reader(file)
	if maximum > 0 {
		reader = io.LimitReader(file, maximum-int64(data.Len())+1)
	}
	if _, err := io.Copy(&data, reader); err != nil {
		return nil, magic.Result{}, false, err
	}
	if maximum > 0 && int64(data.Len()) > maximum {
		return nil, magic.Result{}, true, nil
	}
	content := data.Bytes()
	return content, magic.Detect(content), false, nil
}

func growReadBuffer(file *os.File, data *bytes.Buffer, maximum int64) {
	info, err := file.Stat()
	if err != nil {
		return
	}
	size := info.Size()
	if maximum > 0 {
		size = min(size, maximum+1)
	}
	if size <= int64(data.Len()) || size > maxReadPreallocate {
		return
	}
	data.Grow(int(size) - data.Len())
}

func decodeText(data []byte, detection magic.Result) decodedText {
	switch detection.Encoding {
	case encodingUTF8:
		if !bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
			return decodedText{data: data, encoding: encodingUTF8}
		}
		return decodedText{
			data:       data[utf8BOMSize:],
			offsetBase: utf8BOMSize,
			encoding:   encodingUTF8,
		}
	case encodingUTF16LE:
		return decodeUTF16(data, binary.LittleEndian, encodingUTF16LE)
	case encodingUTF16BE:
		return decodeUTF16(data, binary.BigEndian, encodingUTF16BE)
	}
	if detection.Kind == magic.KindUnknown &&
		detection.Reason == magic.ReasonInvalidText {
		return decodeLatin1(data)
	}
	return decodedText{data: data, encoding: encodingUTF8}
}

func decodeUTF16(data []byte, order binary.ByteOrder, name string) decodedText {
	decoded := decodedText{
		data:     make([]byte, 0, len(data)),
		offsets:  []int{2},
		encoding: name,
	}
	for position := 2; position < len(data); {
		start := position
		var character rune
		if position+1 >= len(data) {
			character = utf8.RuneError
			position++
		} else {
			first := order.Uint16(data[position : position+2])
			position += 2
			character = rune(first)
			if utf16.IsSurrogate(character) {
				if position+1 < len(data) {
					second := rune(order.Uint16(data[position : position+2]))
					if decodedRune := utf16.DecodeRune(character, second); decodedRune != utf8.RuneError {
						character = decodedRune
						position += 2
					} else {
						character = utf8.RuneError
					}
				} else {
					character = utf8.RuneError
				}
			}
		}
		decoded.appendRune(character, start, position)
	}
	return decoded
}

func decodeLatin1(data []byte) decodedText {
	decoded := decodedText{
		data:     make([]byte, 0, len(data)),
		offsets:  []int{0},
		encoding: encodingLatin1,
	}
	for position, value := range data {
		decoded.appendRune(rune(value), position, position+1)
	}
	return decoded
}

func (decoded *decodedText) appendRune(character rune, rawStart, rawEnd int) {
	start := len(decoded.data)
	decoded.data = utf8.AppendRune(decoded.data, character)
	for position := start; position < len(decoded.data); position++ {
		if position == len(decoded.data)-1 {
			decoded.offsets = append(decoded.offsets, rawEnd)
		} else {
			decoded.offsets = append(decoded.offsets, rawStart)
		}
	}
}

func (decoded decodedText) rawOffset(offset int) int {
	if decoded.offsets != nil {
		return decoded.offsets[offset]
	}
	return offset + decoded.offsetBase
}

func remapResultOffsets(result *Result, decoded decodedText) {
	if decoded.offsets == nil && decoded.offsetBase == 0 {
		return
	}
	for detectionIndex := range result.Detections {
		for matchIndex := range result.Detections[detectionIndex].Matches {
			match := &result.Detections[detectionIndex].Matches[matchIndex]
			match.Start = decoded.rawOffset(match.Start)
			match.End = decoded.rawOffset(match.End)
		}
	}
	for matchIndex := range result.Clues {
		match := &result.Clues[matchIndex]
		match.Start = decoded.rawOffset(match.Start)
		match.End = decoded.rawOffset(match.End)
	}
}

func makeFileRecord(
	path string,
	size int64,
	checksum string,
	encoding string,
	text string,
	roles []string,
	licenseTextCoverage float64,
	result Result,
) fileRecord {
	file := fileRecord{
		Path:                path,
		Size:                size,
		SHA256:              checksum,
		Encoding:            encoding,
		Text:                text,
		Roles:               roles,
		LicenseTextCoverage: licenseTextCoverage,
	}
	file.Detections = make([]detectionRecord, 0, len(result.Detections))
	for _, detection := range result.Detections {
		record := detectionRecord{
			Expression:     detection.Expression,
			Identification: detection.Identification,
			Matches:        make([]matchRecord, 0, len(detection.Matches)),
		}
		for _, match := range detection.Matches {
			record.Matches = append(record.Matches, makeMatchRecord(match))
		}
		file.Detections = append(file.Detections, record)
	}
	file.Clues = make([]matchRecord, 0, len(result.Clues))
	for _, clue := range result.Clues {
		file.Clues = append(file.Clues, makeMatchRecord(clue))
	}
	return file
}

type byteRange struct {
	start int
	end   int
}

func calculateLicenseTextCoverage(result Result, inputLength int) float64 {
	if inputLength == 0 {
		return 0
	}

	ranges := make([]byteRange, 0)
	addMatch := func(match Match) {
		if match.Kind != KindText && match.Kind != KindNotice {
			return
		}
		start := max(0, min(match.Start, inputLength))
		end := max(0, min(match.End, inputLength))
		if start >= end {
			return
		}
		ranges = append(ranges, byteRange{start: start, end: end})
	}
	for _, detection := range result.Detections {
		for _, match := range detection.Matches {
			addMatch(match)
		}
	}
	for _, clue := range result.Clues {
		addMatch(clue)
	}
	if len(ranges) == 0 {
		return 0
	}

	slices.SortFunc(ranges, func(first, second byteRange) int {
		if compared := cmp.Compare(first.start, second.start); compared != 0 {
			return compared
		}
		return cmp.Compare(first.end, second.end)
	})
	covered := 0
	current := ranges[0]
	for _, next := range ranges[1:] {
		if next.start <= current.end {
			current.end = max(current.end, next.end)
			continue
		}
		covered += current.end - current.start
		current = next
	}
	covered += current.end - current.start
	return float64(covered) / float64(inputLength) * percentageScale
}

func makeMatchRecord(match Match) matchRecord {
	return matchRecord{
		RuleID:     match.RuleID,
		LicenseIDs: match.LicenseIDs,
		Kind:       match.Kind,
		Method:     match.Method,
		Score:      match.Score,
		Coverage:   match.Coverage,
		Start:      match.Start,
		End:        match.End,
		Matched:    string(match.Matched),
	}
}

func applyScanPolicy(path string, input []byte, result *Result) {
	if len(LegalFileRoles(path)) != 0 {
		return
	}

	documentMarkers := usesDocumentMarkers(path)
	detections := result.Detections[:0]
	for _, detection := range result.Detections {
		matches := detection.Matches[:0]
		for _, match := range detection.Matches {
			if match.Kind == KindReference &&
				crossesBlockBoundary(
					input,
					match.Start,
					match.End,
					documentMarkers,
				) {
				result.Clues = append(result.Clues, match)
				continue
			}
			matches = append(matches, match)
		}
		if len(matches) == 0 {
			continue
		}
		detection.Matches = matches
		detections = append(detections, detection)
	}
	result.Detections = detections
}

func crossesBlockBoundary(
	input []byte,
	start int,
	end int,
	documentMarkers bool,
) bool {
	if start < 0 || start >= end || end > len(input) {
		return false
	}
	for searchStart := start; searchStart < end; {
		relative := bytes.IndexByte(input[searchStart:end], '\n')
		if relative < 0 {
			return false
		}
		newline := searchStart + relative
		leftStart := bytes.LastIndexByte(input[:newline], '\n') + 1
		rightEnd := len(input)
		if next := bytes.IndexByte(input[newline+1:], '\n'); next >= 0 {
			rightEnd = newline + 1 + next
		}
		left := bytes.TrimSpace(input[leftStart:newline])
		right := bytes.TrimSpace(input[newline+1 : rightEnd])
		paragraphLeft, paragraphRight := stripCommonCommentLeader(left, right)
		if len(paragraphLeft) == 0 || len(paragraphRight) == 0 {
			return true
		}
		if documentMarkers && isDocumentBoundary(left, right) {
			return true
		}
		searchStart = newline + 1
	}
	return false
}

func stripCommonCommentLeader(left, right []byte) ([]byte, []byte) {
	for _, leader := range [][]byte{
		[]byte("//"),
		[]byte("--"),
		[]byte("#"),
		[]byte("*"),
		[]byte(";"),
		[]byte("%"),
	} {
		strippedLeft, leftHasLeader := stripCommentLeader(left, leader)
		strippedRight, rightHasLeader := stripCommentLeader(right, leader)
		if leftHasLeader && rightHasLeader {
			return strippedLeft, strippedRight
		}
	}
	return left, right
}

func stripCommentLeader(line, leader []byte) ([]byte, bool) {
	if !bytes.HasPrefix(line, leader) {
		return line, false
	}
	for bytes.HasPrefix(line, leader) {
		line = line[len(leader):]
	}
	return bytes.TrimSpace(line), true
}

func usesDocumentMarkers(filePath string) bool {
	cleaned := filepath.ToSlash(filePath)
	switch strings.ToLower(pathpkg.Ext(cleaned)) {
	case ".md", ".markdown", ".mdown", ".mdx", ".mkd":
		return true
	}
	return strings.EqualFold(pathpkg.Base(cleaned), "readme")
}

func isDocumentBoundary(left, right []byte) bool {
	return isHeadingLine(left) || isHeadingLine(right) ||
		isTableLine(left) || isTableLine(right) ||
		isListItem(right)
}

func isHeadingLine(line []byte) bool {
	return line[0] == '#' || line[0] == '='
}

func isTableLine(line []byte) bool {
	return line[0] == '|' || line[len(line)-1] == '|'
}

func isListItem(line []byte) bool {
	if len(line) < minimumMarkerLength {
		return false
	}
	switch line[0] {
	case '-', '*', '+', '>':
		return line[1] == ' ' || line[1] == '\t'
	}
	index := 0
	for index < len(line) && line[index] >= '0' && line[index] <= '9' {
		index++
	}
	if index == 0 || index+1 >= len(line) ||
		line[index] != '.' && line[index] != ')' {
		return false
	}
	return line[index+1] == ' ' || line[index+1] == '\t'
}

// LegalFileRoles classifies a path as a license, notice, both, or neither.
func LegalFileRoles(filePath string) []string {
	cleaned := filepath.ToSlash(filePath)
	parts := strings.Split(cleaned, "/")
	licenseRole := false
	for _, directory := range parts[:len(parts)-1] {
		switch strings.ToLower(directory) {
		case "license", "licenses", "licence", "licences":
			licenseRole = true
		}
	}

	name := strings.ToLower(pathpkg.Base(cleaned))
	noticeRole := hasLegalNamePrefix(name, "notices") ||
		hasLegalNamePrefix(name, "notice")
	for _, prefix := range []string{
		"licenses",
		"license",
		"licences",
		"licence",
		"copying",
		"mit-license",
		"copyright",
		"unlicense",
	} {
		if hasLegalNamePrefix(name, prefix) {
			licenseRole = true
			break
		}
	}

	roles := make([]string, 0, legalRoleCount)
	if licenseRole {
		roles = append(roles, "license")
	}
	if noticeRole {
		roles = append(roles, "notice")
	}
	return roles
}

func hasLegalNamePrefix(name, prefix string) bool {
	if name == prefix {
		return true
	}
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	switch name[len(prefix)] {
	case '.', '-', '_':
		return true
	default:
		return false
	}
}

func sortFileMatches(file *fileRecord) {
	slices.SortFunc(file.Clues, compareMatchRecords)
	slices.SortFunc(file.Detections, func(first, second detectionRecord) int {
		if compared := compareMatchRecords(first.Matches[0], second.Matches[0]); compared != 0 {
			return compared
		}
		return strings.Compare(first.Expression, second.Expression)
	})
}

func compareMatchRecords(first, second matchRecord) int {
	if compared := cmp.Compare(first.Start, second.Start); compared != 0 {
		return compared
	}
	if compared := cmp.Compare(first.End, second.End); compared != 0 {
		return compared
	}
	if compared := strings.Compare(first.RuleID, second.RuleID); compared != 0 {
		return compared
	}
	return strings.Compare(string(first.Method), string(second.Method))
}

func addExpressionRecords(expressions map[string]*expressionRecord, file fileRecord) {
	root := isRootExpressionFile(file)
	for _, detection := range file.Detections {
		record := expressions[detection.Expression]
		if record == nil {
			record = &expressionRecord{
				Expression:     detection.Expression,
				Identification: detection.Identification,
			}
			expressions[detection.Expression] = record
		}
		record.Root = record.Root || root
		record.Files++
		record.Matches += len(detection.Matches)
	}
}

func isRootExpressionFile(file fileRecord) bool {
	return pathDepth(file.Path) == 1 &&
		(len(file.Roles) != 0 || isReadmeFile(file.Path))
}

func isReadmeFile(filePath string) bool {
	name := strings.ToLower(pathpkg.Base(filepath.ToSlash(filePath)))
	return name == "readme" || strings.HasPrefix(name, "readme.")
}

func addIdentificationSummary(
	summary *scanSummary,
	detections []detectionRecord,
) {
	var identified, partial, noAssertion bool
	for _, detection := range detections {
		switch detection.Identification {
		case Identified:
			identified = true
		case Partial:
			partial = true
		case NoAssertion:
			noAssertion = true
		}
	}
	if identified {
		summary.FilesWithIdentifiedDetections++
	}
	if partial {
		summary.FilesWithPartialDetections++
	}
	if noAssertion {
		summary.FilesWithNoAssertionDetections++
	}
}
