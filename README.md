# licenses

Go library for matching license text against ScanCode's license rule corpus.

The corpus is embedded in the package. Matching needs no network access, cgo,
or Python.

## Installation

```bash
go get github.com/git-pkgs/licenses
```

## Usage

```go
matcher, err := licenses.New()
if err != nil {
	return err
}

result, err := matcher.Match(ctx, text)
if err != nil {
	return err
}

for _, detection := range result.Detections {
	fmt.Println(detection.Expression)
}
```

Matching uses normalized whole-text hashes and exact token sequences. It does
not use fuzzy or sequence matching, so edits within a license can prevent a
match.

## Corpus

The ScanCode commit is pinned in `CORPUS_VERSION`. Regenerate the embedded
index from a clean checkout at that commit:

```bash
go run ./cmd/corpusgen \
  -scancode /path/to/scancode-toolkit \
  -version-file CORPUS_VERSION \
  -output internal/corpus/corpus.bin.gz
```

## Conformance

The exact matcher passes 1,535 of 1,786 cases (85.95%) from ScanCode's four
active data-driven detection suites. Run the suite against a ScanCode checkout
at the commit in `CORPUS_VERSION`:

```bash
SCANCODE_TESTDATA=/path/to/scancode-toolkit/tests/licensedcode/data \
  go test . -run '^TestScanCodeConformanceExact$' -v
```

Known differences are recorded in the conformance baseline. CI fails if an
existing result changes or a new difference appears.

## Benchmarks

Run the matching benchmarks with:

```bash
script/benchmark .
```

Set `BENCH`, `BENCH_TIME`, `BENCH_COUNT`, or `GOMAXPROCS` to override the
defaults.

## License

The Go code is released under the MIT License. ScanCode's license and rule data
is licensed under CC-BY-4.0. See [NOTICE](NOTICE) for attribution and
modification details.
