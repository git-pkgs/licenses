# licenses

Go library and command for matching license text and scanning repositories
against ScanCode's license rule corpus.

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
rule kind, score, coverage, and byte range. Each JSON file record also reports
`license_text_coverage`, the percentage of decoded file bytes covered by the
union of license-text and notice matches. JSON is used when output is
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
schema version 2. Schema 2 is additive: consumers must ignore unknown fields
and accept file records with empty `detections` when `clues` are present. With
`-matched-text`, `matched` contains decoded UTF-8 rather than the original
encoded bytes.

Detection and expression records include `identification`. Its value is
`identified`, `partial`, or SPDX's `NOASSERTION`. Partial expressions contain
both concrete identifiers and ScanCode placeholder `LicenseRef-*` values.
`NOASSERTION` detections confirm license-related text without naming another
license identifier.

JSON reports include a `declared` record for each recognized package manifest
that declares license values or a license-file path. Each record preserves the
raw manifest values and includes a normalized SPDX expression when all values
can be normalized. Multiple values are joined with `OR`; values that cannot be
normalized remain available in `raw` with an empty `normalized_expression`.

Reported expressions use canonical SPDX identifiers when ScanCode supplies
one. Other ScanCode license keys use `LicenseRef-scancode-<key>`. ScanCode rule
IDs remain in each match for traceability.

The three per-file summary counts overlap when one file contains detections in
more than one identification state.

Each match reports the method that produced it. `hash` is a whole-file match
against a rule text, `exact` is a rule token sequence found within the file,
and `spdx-id` is an `SPDX-License-Identifier` tag line whose expression bytes
are not already covered by a rule match. Tag expression grammar is parsed with
`github.com/git-pkgs/spdx`, then bare identifiers are resolved against the
embedded ScanCode mapping and normalized to the primary SPDX identifiers in
ScanCode's metadata. Valid custom `LicenseRef-*` values become
`LicenseRef-scancode-unknown-spdx` and report `NOASSERTION`. Malformed
expressions and unknown bare identifiers do not produce an `spdx-id` match.
Tag matches use the rule id `spdx-license-identifier`.

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

Scan a file or directory with the same matcher:

```go
options := licenses.DefaultScanOptions()
options.IncludeLegalFiles = true
report, err := licenses.ScanRepository(ctx, matcher, ".", options)
if err != nil {
	return err
}

for _, file := range report.Files {
	fmt.Println(file.Path, file.Roles, file.Text)
}
```

`IncludeLegalFiles` retains recognized license and notice files when the corpus
does not produce a match. It also includes their complete text decoded to
UTF-8.

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

See [docs/benchmarking.md](docs/benchmarking.md) for matcher, repository
scanner, and Licensee comparison benchmarks.

## License

The Go code is released under the MIT License. ScanCode's license and rule data
is licensed under CC-BY-4.0. See [NOTICE](NOTICE) for attribution and
modification details.
