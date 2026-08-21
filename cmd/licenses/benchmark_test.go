package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
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
			testScannerVersion,
		)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScanRepositories(b *testing.B) {
	roots := benchmarkRepositoryRoots(b)
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
	for _, root := range roots {
		b.Run(filepath.Base(filepath.Clean(root)), func(b *testing.B) {
			report, err := scanRepository(
				context.Background(),
				matcher,
				root,
				options,
				testScannerVersion,
			)
			if err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for b.Loop() {
				benchmarkScanReport, err = scanRepository(
					context.Background(),
					matcher,
					root,
					options,
					testScannerVersion,
				)
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.SetBytes(report.Summary.BytesScanned)
			b.ReportMetric(float64(report.Summary.FilesScanned), "files/op")
		})
	}
}

func BenchmarkRepositoryCLIs(b *testing.B) {
	roots := benchmarkRepositoryRoots(b)
	licensesBinary := os.Getenv("LICENSES_BENCH_BIN")
	if licensesBinary == "" {
		b.Skip("set LICENSES_BENCH_BIN to a built cmd/licenses binary")
	}
	licenseeBinary := os.Getenv("LICENSEE_BENCH_BIN")
	if licenseeBinary == "" {
		var err error
		licenseeBinary, err = exec.LookPath("licensee")
		if err != nil {
			b.Skip("licensee is not installed and LICENSEE_BENCH_BIN is unset")
		}
	}

	b.Run("licenses", func(b *testing.B) {
		benchmarkLicensesCLI(b, licensesBinary, roots)
	})

	b.Run("licensee", func(b *testing.B) {
		benchmarkLicenseeCLI(b, licenseeBinary, roots)
	})
}

func benchmarkLicensesCLI(b *testing.B, binary string, roots []string) {
	b.Helper()
	for _, root := range roots {
		b.Run(filepath.Base(filepath.Clean(root)), func(b *testing.B) {
			var output bytes.Buffer
			peakRSS, err := runLicensesCLI(
				b.Context(),
				binary,
				root,
				&output,
			)
			if err != nil {
				b.Fatal(err)
			}
			var report scanReport
			if err := json.Unmarshal(output.Bytes(), &report); err != nil {
				b.Fatalf("decode licenses output: %v", err)
			}
			b.ResetTimer()
			for b.Loop() {
				if _, err := runLicensesCLI(
					b.Context(),
					binary,
					root,
					io.Discard,
				); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(report.Summary.FilesScanned), "files/op")
			b.ReportMetric(
				float64(report.Summary.FilesWithDetections),
				"detected_files/op",
			)
			b.ReportMetric(bytesToMiB(peakRSS), "peak_MiB")
			b.ReportMetric(bytesToMiB(int64(output.Len())), "output_MiB")
		})
	}
}

func benchmarkLicenseeCLI(b *testing.B, binary string, roots []string) {
	b.Helper()
	for _, root := range roots {
		b.Run(filepath.Base(filepath.Clean(root)), func(b *testing.B) {
			var output bytes.Buffer
			peakRSS, err := runLicenseeCLI(
				b.Context(),
				binary,
				root,
				&output,
			)
			if err != nil {
				b.Fatal(err)
			}
			var report licenseeReport
			if output.Len() > 0 {
				if err := json.Unmarshal(output.Bytes(), &report); err != nil {
					b.Fatalf("decode Licensee output: %v", err)
				}
			}
			b.ResetTimer()
			for b.Loop() {
				if _, err := runLicenseeCLI(
					b.Context(),
					binary,
					root,
					io.Discard,
				); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(len(report.MatchedFiles)), "matched_files/op")
			b.ReportMetric(float64(len(report.Licenses)), "licenses/op")
			b.ReportMetric(bytesToMiB(peakRSS), "peak_MiB")
			b.ReportMetric(bytesToMiB(int64(output.Len())), "output_MiB")
		})
	}
}

func benchmarkRepositoryRoots(b *testing.B) []string {
	b.Helper()
	value := os.Getenv("LICENSES_BENCH_REPOS")
	if value == "" {
		b.Skip("set LICENSES_BENCH_REPOS to a path-list of repositories")
	}
	var roots []string
	for _, root := range filepath.SplitList(value) {
		root = strings.TrimSpace(root)
		if root != "" {
			roots = append(roots, root)
		}
	}
	if len(roots) == 0 {
		b.Skip("LICENSES_BENCH_REPOS contains no repository paths")
	}
	return roots
}

func runLicensesCLI(
	ctx context.Context,
	binary string,
	root string,
	stdout io.Writer,
) (int64, error) {
	command := exec.CommandContext(ctx, binary, "-json", root)
	command.Stdout = stdout
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == exitNoDetections {
			return processMaxRSS(command.ProcessState), nil
		}
		return 0, fmt.Errorf(
			"run licenses for %s: %w: %s",
			root,
			err,
			strings.TrimSpace(stderr.String()),
		)
	}
	return processMaxRSS(command.ProcessState), nil
}

func runLicenseeCLI(
	ctx context.Context,
	binary string,
	root string,
	stdout io.Writer,
) (int64, error) {
	command := exec.CommandContext(
		ctx,
		binary,
		"detect",
		"--json",
		"--filesystem",
		root,
	)
	command.Stdout = stdout
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return processMaxRSS(command.ProcessState), nil
		}
		return 0, fmt.Errorf(
			"run Licensee for %s: %w: %s",
			root,
			err,
			strings.TrimSpace(stderr.String()),
		)
	}
	return processMaxRSS(command.ProcessState), nil
}

func processMaxRSS(state *os.ProcessState) int64 {
	if state == nil {
		return 0
	}
	usage := reflect.ValueOf(state.SysUsage())
	if !usage.IsValid() {
		return 0
	}
	if usage.Kind() == reflect.Pointer {
		usage = usage.Elem()
	}
	if !usage.IsValid() || usage.Kind() != reflect.Struct {
		return 0
	}
	maxRSS := usage.FieldByName("Maxrss")
	if !maxRSS.IsValid() || !maxRSS.CanInt() {
		return 0
	}
	return normalizeMaxRSS(runtime.GOOS, maxRSS.Int())
}

func normalizeMaxRSS(goos string, value int64) int64 {
	if goos == "linux" {
		return value * 1024
	}
	return value
}

func bytesToMiB(value int64) float64 {
	const mebibyte = 1 << 20
	return float64(value) / mebibyte
}
