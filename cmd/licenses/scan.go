package main

import (
	"context"
	"fmt"

	licenses "github.com/git-pkgs/licenses"
)

const (
	reportSchemaVersion = licenses.ReportSchemaVersion
	defaultMaxDepth     = licenses.DefaultMaxDepth
	defaultMaxFiles     = licenses.DefaultMaxFiles
	defaultMaxFileSize  = licenses.DefaultMaxFileSize
	scannerName         = licenses.ScannerName
	scopeAll            = licenses.ScopeAll
	scopeProject        = licenses.ScopeProject
)

type scanOptions = licenses.ScanOptions
type scanReport = licenses.ScanReport
type scannerRecord = licenses.ScannerRecord
type scanSummary = licenses.ScanSummary
type expressionRecord = licenses.ExpressionRecord
type fileRecord = licenses.FileRecord
type detectionRecord = licenses.DetectionRecord
type matchRecord = licenses.MatchRecord
type scanErrorRecord = licenses.ScanErrorRecord
type skipRecord = licenses.SkipRecord

func defaultWorkerCount() int {
	return licenses.DefaultScanOptions().Workers
}

func scanRepository(
	ctx context.Context,
	matcher *licenses.Matcher,
	root string,
	options scanOptions,
	scannerVersion string,
) (scanReport, error) {
	options.ScannerVersion = scannerVersion
	return licenses.ScanRepository(ctx, matcher, root, options)
}

func validateScanOptions(options scanOptions) error {
	return licenses.ValidateScanOptions(options)
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
