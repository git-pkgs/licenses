# Benchmarking

The benchmarks use Go's `testing` package. Record the Go version, operating
system, corpus commit, and Licensee version with published results.

## Matcher

Run the byte-slice matching benchmarks on one logical processor:

```bash
GOMAXPROCS=1 go test \
  -run '^$' \
  -bench . \
  -benchmem \
  -benchtime 1s \
  -count 5 \
  .
```

Cold `New` and repeated `New` are separate benchmarks. Matching benchmarks
reuse one loaded corpus.

## Repository scanner

Run the generated repository benchmark with:

```bash
go test ./cmd/licenses \
  -run '^$' \
  -bench '^BenchmarkScanRepository$' \
  -benchmem \
  -count 5
```

Set `LICENSES_BENCH_REPOS` to benchmark local checkouts with the scanner's
default project scope:

```bash
LICENSES_BENCH_REPOS=/path/to/repo1:/path/to/repo2 \
  go test ./cmd/licenses \
  -run '^$' \
  -bench '^BenchmarkScanRepositories$' \
  -benchmem \
  -count 5
```

`BenchmarkScanRepositories` measures warm in-process scans. Every repository
uses the same loaded matcher.

## Licensee comparison

Licensee must be installed before running the comparison. Build the current
`licenses` command once, then provide both the binary and repository list:

```bash
go build -o /tmp/licenses-bench ./cmd/licenses

export LICENSES_BENCH_REPOS=/path/to/repo1:/path/to/repo2
export LICENSES_BENCH_BIN=/tmp/licenses-bench

go test ./cmd/licenses \
  -run '^TestCompareLicenseeRepositories$' \
  -v

go test ./cmd/licenses \
  -run '^$' \
  -bench '^BenchmarkRepositoryCLIs$' \
  -benchtime 3x \
  -count 5
```

Set `LICENSEE_BENCH_BIN` when `licensee` is not on `PATH`. The result comparison
uses root legal files and README files for the direct text comparison.
Detections elsewhere in the repository and Licensee's manifest-derived
licenses are printed separately.

The command benchmark includes process startup and JSON encoding for both
tools. It reports output size and peak resident memory from an untimed first
run. Allocation figures from `-benchmem` cover the Go benchmark driver, not
memory allocated by either child process.

Use the standard `go test` flags to change benchmark duration and sample count.
