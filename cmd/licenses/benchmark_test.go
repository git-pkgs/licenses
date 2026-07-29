package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	licenses "github.com/git-pkgs/licenses"
)

var benchmarkScanReport scanReport

func BenchmarkScanRepository(b *testing.B) {
	root := b.TempDir()
	data, err := os.ReadFile("../../LICENSE")
	if err != nil {
		b.Fatal(err)
	}
	for index := range 100 {
		path := filepath.Join(root, "licenses", strconv.Itoa(index), "LICENSE")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			b.Fatal(err)
		}
	}
	matcher, err := licenses.New()
	if err != nil {
		b.Fatal(err)
	}
	options := scanOptions{
		MaxDepth:    defaultMaxDepth,
		MaxFiles:    defaultMaxFiles,
		MaxFileSize: defaultMaxFileSize,
		Workers:     defaultWorkerCount(),
	}

	b.SetBytes(int64(len(data) * 100))
	b.ResetTimer()
	for b.Loop() {
		benchmarkScanReport, err = scanRepository(
			context.Background(),
			matcher,
			root,
			options,
		)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScanRepositories(b *testing.B) {
	value := os.Getenv("LICENSES_BENCH_REPOS")
	if value == "" {
		b.Skip("set LICENSES_BENCH_REPOS to a path-list of repositories")
	}
	matcher, err := licenses.New()
	if err != nil {
		b.Fatal(err)
	}
	options := scanOptions{
		MaxDepth:    defaultMaxDepth,
		MaxFiles:    defaultMaxFiles,
		MaxFileSize: defaultMaxFileSize,
		Workers:     defaultWorkerCount(),
	}
	for _, root := range filepath.SplitList(value) {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		b.Run(filepath.Base(filepath.Clean(root)), func(b *testing.B) {
			report, err := scanRepository(
				context.Background(),
				matcher,
				root,
				options,
			)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(report.Summary.BytesScanned)
			b.ReportMetric(float64(report.Summary.FilesScanned), "files/op")
			b.ResetTimer()
			for b.Loop() {
				benchmarkScanReport, err = scanRepository(
					context.Background(),
					matcher,
					root,
					options,
				)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
