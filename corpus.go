// Package licenses matches text and scans repositories against the ScanCode
// license rule corpus. Matching is exact after token normalization, so edits
// within a license can prevent a match.
package licenses

// CorpusInfo identifies the ScanCode corpus used for a result.
type CorpusInfo struct {
	Version      string // ScanCode version recorded in CORPUS_VERSION.
	RuleCount    int    // Number of license texts and rules in the index.
	SourceCommit string // Full ScanCode Toolkit source commit.
}
