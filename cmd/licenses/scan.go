package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
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

	licenses "github.com/git-pkgs/licenses"
	"github.com/git-pkgs/magic"
)

const (
	reportSchemaVersion     = 1
	defaultMaxDepth         = 32
	defaultMaxFiles         = 10_000
	defaultMaxFileSize      = 1 << 20
	classificationProbeSize = 8 << 10
	maxWorkers              = 16
	maxReadPreallocate      = 16 << 20
	utf8BOMSize             = 3
	minimumMarkerLength     = 2
	encodingUTF8            = "utf-8"
	encodingUTF16LE         = "utf-16le"
	encodingUTF16BE         = "utf-16be"
	encodingLatin1          = "iso-8859-1"

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

type scanOptions struct {
	MaxDepth      int
	MaxFiles      int
	MaxFileSize   int64
	Workers       int
	SkipDirs      map[string]bool
	NoDefaultSkip bool
}

type scanReport struct {
	Schema      int                `json:"schema"`
	Root        string             `json:"root"`
	Scope       string             `json:"scope"`
	Corpus      corpusRecord       `json:"corpus"`
	Summary     scanSummary        `json:"summary"`
	Expressions []expressionRecord `json:"expressions"`
	Files       []fileRecord       `json:"files"`
	Skipped     []skipRecord       `json:"skipped"`
	Errors      []scanErrorRecord  `json:"errors"`
}

type corpusRecord struct {
	Version      string `json:"version"`
	RuleCount    int    `json:"rule_count"`
	SourceCommit string `json:"source_commit"`
}

type scanSummary struct {
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

type expressionRecord struct {
	Expression     string                  `json:"expression"`
	Identification licenses.Identification `json:"identification"`
	Files          int                     `json:"files"`
	Matches        int                     `json:"matches"`
}

type fileRecord struct {
	Path       string            `json:"path"`
	Size       int64             `json:"size"`
	Encoding   string            `json:"encoding"`
	Detections []detectionRecord `json:"detections"`
	Clues      []matchRecord     `json:"clues"`
}

type detectionRecord struct {
	Expression     string                  `json:"expression"`
	Identification licenses.Identification `json:"identification"`
	Matches        []matchRecord           `json:"matches"`
}

type matchRecord struct {
	RuleID     string          `json:"rule_id"`
	LicenseIDs []string        `json:"license_ids,omitempty"`
	Kind       licenses.Kind   `json:"kind"`
	Method     licenses.Method `json:"method"`
	Score      float64         `json:"score"`
	Coverage   float64         `json:"coverage"`
	Start      int             `json:"start"`
	End        int             `json:"end"`
	Matched    string          `json:"matched,omitempty"`
}

type scanErrorRecord struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

type skipRecord struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type fileTask struct {
	path       string
	display    string
	policyPath string
}

type fileOutcome struct {
	task     fileTask
	result   licenses.Result
	bytes    int64
	scanned  bool
	binary   bool
	tooLarge bool
	encoding string
	err      error
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
	scanErrors []scanErrorRecord
	skipped    []skipRecord
}

func defaultWorkerCount() int {
	return min(runtime.GOMAXPROCS(0), maxWorkers)
}

func scanRepository(
	ctx context.Context,
	matcher *licenses.Matcher,
	root string,
	options scanOptions,
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
		Schema:  reportSchemaVersion,
		Root:    filepath.Clean(root),
		Scope:   scanScope(options),
		Files:   make([]fileRecord, 0),
		Skipped: make([]skipRecord, 0),
		Errors:  make([]scanErrorRecord, 0),
		Corpus: corpusRecord{
			Version:      corpus.Version,
			RuleCount:    corpus.RuleCount,
			SourceCommit: corpus.SourceCommit,
		},
	}
	tasks, walkErrors, skipped, err := discoverFiles(ctx, root, options, &report.Summary)
	if err != nil {
		return scanReport{}, err
	}
	report.Errors = append(report.Errors, walkErrors...)
	report.Skipped = append(report.Skipped, skipped...)

	outcomes := scanFiles(ctx, matcher, tasks, options)
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
			file := makeFileRecord(
				outcome.task.display,
				outcome.bytes,
				outcome.encoding,
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
			if len(file.Detections) == 0 && len(file.Clues) == 0 {
				continue
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
) ([]fileTask, []scanErrorRecord, []skipRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, nil, nil, err
	}
	if !info.IsDir() {
		return discoverExplicitFile(root, info, options, summary)
	}
	walkRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, nil, nil, err
	}

	discovery := fileDiscovery{
		ctx:     ctx,
		root:    walkRoot,
		options: options,
		summary: summary,
	}
	err = filepath.WalkDir(walkRoot, discovery.visit)
	if err != nil && !errors.Is(err, errFileLimit) {
		return nil, nil, nil, err
	}
	return discovery.tasks, discovery.scanErrors, discovery.skipped, nil
}

func discoverExplicitFile(
	path string,
	info os.FileInfo,
	options scanOptions,
	summary *scanSummary,
) ([]fileTask, []scanErrorRecord, []skipRecord, error) {
	summary.FilesVisited = 1
	if !info.Mode().IsRegular() {
		summary.FilesSkippedOther = 1
		return nil, nil, []skipRecord{{
			Path:   filepath.Base(path),
			Reason: skipReasonNonRegular,
		}}, nil
	}
	if options.MaxFileSize > 0 && info.Size() > options.MaxFileSize {
		summary.FilesSkippedSize = 1
		return nil, nil, []skipRecord{{
			Path:   filepath.Base(path),
			Reason: skipReasonSize,
		}}, nil
	}
	return []fileTask{{
		path:       path,
		display:    filepath.Base(path),
		policyPath: explicitPolicyPath(path),
	}}, nil, nil, nil
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
	return nil
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
	matcher *licenses.Matcher,
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
				outcome := scanFile(ctx, matcher, task, options.MaxFileSize)
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
	matcher *licenses.Matcher,
	task fileTask,
	maxFileSize int64,
) fileOutcome {
	data, detection, tooLarge, err := readScannableFile(task.path, maxFileSize)
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
	remapResultOffsets(&result, decoded)
	return fileOutcome{
		task:     task,
		result:   result,
		bytes:    int64(len(data)),
		scanned:  true,
		encoding: decoded.encoding,
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

func remapResultOffsets(result *licenses.Result, decoded decodedText) {
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
	encoding string,
	result licenses.Result,
) fileRecord {
	file := fileRecord{Path: path, Size: size, Encoding: encoding}
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

func makeMatchRecord(match licenses.Match) matchRecord {
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

func applyScanPolicy(path string, input []byte, result *licenses.Result) {
	if isLegalFile(path) {
		return
	}

	documentMarkers := usesDocumentMarkers(path)
	detections := result.Detections[:0]
	for _, detection := range result.Detections {
		matches := detection.Matches[:0]
		for _, match := range detection.Matches {
			if match.Kind == licenses.KindReference &&
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

func isLegalFile(filePath string) bool {
	cleaned := filepath.ToSlash(filePath)
	parts := strings.Split(cleaned, "/")
	for _, directory := range parts[:len(parts)-1] {
		switch strings.ToLower(directory) {
		case "license", "licenses", "licence", "licences":
			return true
		}
	}

	name := strings.ToLower(pathpkg.Base(cleaned))
	for _, prefix := range []string{
		"licenses",
		"license",
		"licences",
		"licence",
		"copying",
		"mit-license",
		"notices",
		"notice",
		"copyright",
		"unlicense",
	} {
		if hasLegalNamePrefix(name, prefix) {
			return true
		}
	}
	return false
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
	for _, detection := range file.Detections {
		record := expressions[detection.Expression]
		if record == nil {
			record = &expressionRecord{
				Expression:     detection.Expression,
				Identification: detection.Identification,
			}
			expressions[detection.Expression] = record
		}
		record.Files++
		record.Matches += len(detection.Matches)
	}
}

func addIdentificationSummary(
	summary *scanSummary,
	detections []detectionRecord,
) {
	var identified, partial, noAssertion bool
	for _, detection := range detections {
		switch detection.Identification {
		case licenses.Identified:
			identified = true
		case licenses.Partial:
			partial = true
		case licenses.NoAssertion:
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

func formatBytes(size int64) string {
	const (
		kib = 1 << 10
		mib = 1 << 20
	)
	switch {
	case size >= mib:
		return fmt.Sprintf("%.1f MiB", float64(size)/mib)
	case size >= kib:
		return fmt.Sprintf("%.1f KiB", float64(size)/kib)
	default:
		return fmt.Sprintf("%d B", size)
	}
}
