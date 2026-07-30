# licenses

Go library for matching license text against ScanCode's license rule corpus.

The corpus is embedded in the package. Matching needs no network access, cgo,
or Python.

## Install

Install the repository scanner with Homebrew:

```bash
brew tap git-pkgs/git-pkgs
brew trust --tap git-pkgs/git-pkgs
brew install licenses
```

Or with Go:

```bash
go install github.com/git-pkgs/licenses/cmd/licenses@latest
```

Add the matching library to a Go module:

```bash
go get github.com/git-pkgs/licenses
```

## Scan a repository

```bash
licenses .
licenses /path/to/repository
licenses -json /path/to/repository > licenses.json
licenses -scope all -max-files 0 /path/to/repository
```

The command reports detections by file with the matching rule, expression,
rule kind, score, coverage, and byte range. JSON is used when output is
redirected; terminals get a text report. Skipped files and directories are
named with the reason they were skipped.

Reference rules that join text across document blocks are reported as clues
outside files such as `LICENSE`, `COPYING`, and `NOTICE`. Soft-wrapped lines,
including lines with a shared source-comment prefix, remain detections.
Demoted matches stay visible in the file report but do not contribute to the
repository expression totals.

The default `project` scope skips hidden, dependency, build, cache, and
test-data directories. Use `-scope all` for dependency-license scans. An
explicit `-skip` list applies in either scope. Both scopes exclude `.git`.

Regular text files are limited to 1 MiB and 32 directory levels. Project scope
also has a 10,000-file default limit. All scope requires an explicit
`-max-files` value; use `-max-files 0` for an unlimited dependency scan.
Setting any limit to zero removes that guard.

The scanner accepts UTF-8, UTF-16LE or UTF-16BE with a byte-order mark, and
Latin-1. Reported byte ranges refer to the original file. JSON reports use
schema version 1. Schema 1 is additive: consumers must ignore unknown fields
and accept file records with empty `detections` when `clues` are present. With
`-matched-text`, `matched` contains decoded UTF-8 rather than the original
encoded bytes.

Detection and expression records include `identification`. Its value is
`identified`, `partial`, or SPDX's `NOASSERTION`. Partial expressions contain
both non-placeholder and ScanCode placeholder identifiers. `NOASSERTION`
detections confirm license-related text without naming another license
identifier.

The three per-file summary counts overlap when one file contains detections in
more than one identification state.

Each match reports the method that produced it. `hash` is a whole-file match
against a rule text, `exact` is a rule token sequence found within the file,
and `spdx-id` is an `SPDX-License-Identifier` tag line whose expression bytes
are not already covered by a rule match. Tag expressions are parsed strictly
with `github.com/git-pkgs/spdx` and reported using ScanCode license keys, so
`BSD-3-Clause` becomes `bsd-new`. Valid custom `LicenseRef-*` values become
`unknown-spdx` and report `NOASSERTION`. Malformed expressions and unknown
bare identifiers do not produce an `spdx-id` match. Tag matches use the rule
id `spdx-license-identifier`.

Exit status 0 means detections were found, 1 is a fatal command error, 2 means
the scan was incomplete because of per-file errors or the file limit, and 3
means no conclusive detections were found.

## Use the library

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

Matching uses normalized whole-text hashes, exact token sequences, and
`SPDX-License-Identifier` tag lines. It does not use fuzzy or sequence
matching, so edits within a license text can prevent a match.

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
GOMAXPROCS=1 go test \
  -run '^$' \
  -bench . \
  -benchmem \
  -benchtime 1s \
  -count 5 \
  .
```

Run the repository scan benchmark with:

```bash
go test ./cmd/licenses \
  -run '^$' \
  -bench '^BenchmarkScanRepository$' \
  -benchmem \
  -count 5
```

To benchmark local checkouts with the same scan defaults:

```bash
LICENSES_BENCH_REPOS=/path/to/repo1:/path/to/repo2 \
  go test ./cmd/licenses \
  -run '^$' \
  -bench '^BenchmarkScanRepositories$' \
  -benchmem \
  -count 5
```

Use the standard `go test` flags to select benchmarks or change their duration
and sample count.

## License

The Go code is released under the MIT License. ScanCode's license and rule data
is licensed under CC-BY-4.0. See [NOTICE](NOTICE) for attribution and
modification details.
