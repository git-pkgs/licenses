# Benchmarking

The benchmarks use Go's `testing` package. Record the Go version, operating
system, corpus commit, and Licensee or scancode-toolkit version with
published results.

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

## ScanCode comparison

Install the current scancode-toolkit in an isolated environment and let it
build its license index once before timing:

```bash
python3 -m venv /tmp/scancode-venv
/tmp/scancode-venv/bin/pip install scancode-toolkit
/tmp/scancode-venv/bin/scancode --version
```

Time both tools against the same checkout:

```bash
go build -o /tmp/licenses-bench ./cmd/licenses

/usr/bin/time -l /tmp/licenses-bench -json /path/to/repo > /dev/null
/usr/bin/time -l /tmp/scancode-venv/bin/scancode -l \
  --json-pp /tmp/scancode.json /path/to/repo
```

`licenses` exits 2 when the scan completes with per-file errors, such
as manifests its parser does not model or a repository's own
deliberately-invalid test fixtures. Timing and peak RSS are unaffected.

`scancode -l` restricts scancode-toolkit to license detection. Its default
process count is one less than the CPU count from 32.4.0 onward; pass `-n 1`
for a single-worker run.

`/usr/bin/time -l` on macOS and `-v` on GNU time report peak resident memory
for the parent process only. scancode forks worker processes that each load
the license index, so aggregate memory needs an external sampler. The README
figures were collected by polling `ps -axo pid,ppid,rss,command` at 200 ms
intervals across the process tree and taking the maximum sum. rust-lang/cargo
was measured at commit `a07c49a989d565727725e5bb5a8038ff402006a8`.

## Corpus regeneration report

The monthly corpus refresh workflow benchmarks the checked-in and regenerated
corpora back to back on the same runner. It records the median cold startup,
repeated `New`, and warm corpus-matching results, then combines them with corpus
identity, size, deterministic rebuild, and conformance changes in the refresh
pull request description.

`cmd/corpusreport` renders the Markdown report from the two corpus files,
conformance baselines, deterministic rebuild, and Go benchmark outputs. The
workflow is the reference invocation because it captures the checked-in inputs
before regeneration.
