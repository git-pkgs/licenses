# licenses

`licenses` matches byte slices against the ScanCode license rule corpus. It is
a Go library with no runtime network access, cgo, Python, or filesystem scan.

```go
matcher, err := licenses.New()
if err != nil {
	return err
}
result, err := matcher.Match(ctx, text)
if err != nil {
	return err
}
```

The matcher supports normalized whole-text hash matches and exact
token-sequence matches, followed by ScanCode-style containment and overlap
filtering. It does not perform sequence alignment or fuzzy matching.
`Matcher` shares one read-only decoded corpus per process and is safe for
concurrent calls to `Match`. A reflowed license, an edited paragraph, or text
with a copyright line spliced into the middle may not match.

The corpus is pinned in `CORPUS_VERSION` and committed as one embedded binary
index. Regenerate it from a clean checkout at that commit:

```sh
go run ./cmd/corpusgen \
  -scancode /path/to/scancode-toolkit \
  -version-file CORPUS_VERSION \
  -output internal/corpus/corpus.bin.gz
```

Corpus attribution and modification details are in `NOTICE`.

Run usage benchmarks with fixed single-core defaults and five samples:

```sh
BENCH='Matcher|Match' script/benchmark .
```

`BENCH`, `BENCH_TIME`, `BENCH_COUNT`, and `GOMAXPROCS` can override the
defaults. Save stdout from two revisions to compare their results over time.
Cold corpus loading, decompression, and failure-link construction have separate
benchmarks under `./internal/corpus`.

The exact-core baseline passes 1,535 of 1,786 cases (85.95%) from ScanCode's
four active data-driven detection suites. Run it against a checkout at the
commit in `CORPUS_VERSION`:

```sh
SCANCODE_TESTDATA=/path/to/scancode-toolkit/tests/licensedcode/data \
  go test . -run '^TestScanCodeConformanceExact$' -v
```

The remaining cases are recorded as exact-core won't-fix divergences, with the
stage and ScanCode source commit attached. CI rejects a new divergence or a
change to an existing one, even if the aggregate pass count rises. Set
`UPDATE_CONFORMANCE=1` only after an intentional matcher change.
