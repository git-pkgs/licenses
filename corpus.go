// Package licenses matches byte slices against the ScanCode license rule corpus.
package licenses

// CorpusInfo identifies the ScanCode corpus used for a result.
type CorpusInfo struct {
	Version      string // ScanCode version recorded in CORPUS_VERSION.
	RuleCount    int    // Number of license texts and rules in the index.
	SourceCommit string // Full ScanCode Toolkit source commit.
}
