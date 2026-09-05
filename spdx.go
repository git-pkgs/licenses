package licenses

import (
	"bytes"
	"cmp"
	"slices"
	"sort"
	"strings"

	"github.com/git-pkgs/licenses/internal/corpus"
	"github.com/git-pkgs/spdx"
)

const (
	spdxTagRuleID            = "spdx-license-identifier"
	licenseRefScancode       = "LicenseRef-scancode-"
	licenseRefScancodePrefix = "licenseref-scancode-"
	unknownSPDXKey           = "unknown-spdx"
	maxSPDXExpressionBytes   = 1024
)

// spdxTags lists the tag spellings recognised as SPDX identifier lines.
// ScanCode also accepts the common "identifer" typo.
var spdxTags = []string{
	"spdx-license-identifier",
	"spdx-license-identifer",
}

// spdxIndex maps SPDX identifiers to the ScanCode keys used for matching, and
// ScanCode keys to the SPDX-compatible identifiers used in public results.
type spdxIndex struct {
	keys         map[string]string
	reportingIDs map[string]string
}

// buildSPDXIndex constructs the SPDX identifier map. SPDXKeys from the corpus
// is authoritative; every identifier that appears in a rule expression maps to
// itself when not already covered.
func buildSPDXIndex(index corpus.Index) spdxIndex {
	keys := make(map[string]string, len(index.SPDXKeys))
	for spdx, scancode := range index.SPDXKeys {
		keys[spdx] = scancode
	}
	reportingIDs := make(map[string]string, len(index.ReportingIDs))
	for scancode, identifier := range index.ReportingIDs {
		reportingIDs[strings.ToLower(scancode)] = identifier
	}
	for _, rule := range index.Rules {
		if rule.Flags&corpus.FlagFalsePositive != 0 {
			continue
		}
		for _, identifier := range expressionIDs(rule.Expression) {
			lower := strings.ToLower(identifier)
			if _, exists := keys[lower]; !exists {
				keys[lower] = lower
			}
			if _, exists := reportingIDs[lower]; !exists {
				reportingIDs[lower] = licenseRefScancode + lower
			}
		}
	}
	return spdxIndex{keys: keys, reportingIDs: reportingIDs}
}

// resolve returns the ScanCode key for an SPDX identifier token. Unknown bare
// identifiers are rejected, while valid custom license references resolve to
// unknown-spdx.
func (index spdxIndex) resolve(identifier string) (string, bool) {
	lower := strings.ToLower(identifier)
	if key, ok := index.keys[lower]; ok {
		return key, true
	}
	if scancode, ok := strings.CutPrefix(lower, licenseRefScancodePrefix); ok {
		if key, ok := index.keys[scancode]; ok {
			return key, true
		}
	}
	if strings.HasPrefix(lower, "licenseref-") ||
		strings.HasPrefix(lower, "documentref-") {
		return unknownSPDXKey, true
	}
	return "", false
}

// report returns the canonical SPDX identifier for a ScanCode key, falling
// back to ScanCode's LicenseRef namespace when the key has no mapping.
func (index spdxIndex) report(identifier string) string {
	lower := strings.ToLower(identifier)
	if reportingID, ok := index.reportingIDs[lower]; ok {
		return reportingID
	}
	return licenseRefScancode + lower
}

// reportExpression rewrites the ScanCode keys in expression to their public
// SPDX identifiers and returns the distinct identifiers in encounter order.
func (index spdxIndex) reportExpression(expression string) (string, []string) {
	var identifiers []string
	rewritten := rewriteExpressionIdentifiers(expression, func(identifier string) string {
		reportingID := index.report(identifier)
		if !slices.Contains(identifiers, reportingID) {
			identifiers = append(identifiers, reportingID)
		}
		return reportingID
	})
	return rewritten, identifiers
}

type spdxSpan struct {
	start, end int
}

type spdxDeclaration struct {
	anchor, start, end int
	covered            bool
}

// matchSPDXTags preserves complete declarations over their component matches.
func (m *Matcher) matchSPDXTags(input []byte, result *Result) bool {
	first := indexSPDXAnchor(input, 0)
	if first < 0 {
		return false
	}
	coverage := spdxMatchSpans(result)
	next, coveredEnd := 0, 0
	var declarations []spdxDeclaration
	pending := make(map[string]*Detection)
	for offset := first; offset < len(input); {
		anchor := indexSPDXAnchor(input, offset)
		if anchor < 0 {
			break
		}
		tagEnd := spdxTagEnd(input, anchor)
		if tagEnd < 0 {
			offset = anchor + 1
			continue
		}
		start, end := spdxExpressionSpan(input, tagEnd)
		offset = max(end, tagEnd)
		if start >= end {
			continue
		}
		for next < len(coverage) && coverage[next].start <= start {
			coveredEnd = max(coveredEnd, coverage[next].end)
			next++
		}
		if coveredEnd >= end {
			declarations = append(declarations, spdxDeclaration{anchor: anchor, start: start, end: end, covered: true})
			continue
		}
		expression, identifiers, scanCodeIDs := m.engine.spdx.normalizeExpression(input[start:end])
		if expression == "" {
			continue
		}
		declarations = append(declarations, spdxDeclaration{anchor: anchor, start: start, end: end})
		match := Match{
			RuleID: spdxTagRuleID, LicenseIDs: identifiers, Kind: KindTag, Method: SpdxID,
			Score: fullScore, Coverage: fullScore, Start: anchor, End: end,
		}
		if m.matchedText {
			match.Matched = slices.Clone(input[anchor:end])
		}
		detection := pending[expression]
		if detection == nil {
			detection = &Detection{Expression: expression, Identification: identificationForIDs(scanCodeIDs)}
			pending[expression] = detection
		}
		detection.Matches = append(detection.Matches, match)
	}
	changed := removeMatches(result, func(match Match) bool {
		index := sort.Search(len(declarations), func(i int) bool { return declarations[i].end >= match.End })
		if index == len(declarations) {
			return false
		}
		declaration := declarations[index]
		return match.Start >= declaration.anchor &&
			(!declaration.covered || match.Start > declaration.start || match.End < declaration.end)
	})
	if len(pending) == 0 {
		return changed
	}
	for index := range result.Detections {
		detection := &result.Detections[index]
		if addition := pending[detection.Expression]; addition != nil {
			detection.Matches = append(detection.Matches, addition.Matches...)
			delete(pending, detection.Expression)
		}
	}
	for _, detection := range pending {
		result.Detections = append(result.Detections, *detection)
	}
	return true
}

// Earlier declarations cannot remove a match extending over a later expression.
func spdxMatchSpans(result *Result) []spdxSpan {
	var spans []spdxSpan
	for _, detection := range result.Detections {
		for _, match := range detection.Matches {
			spans = append(spans, spdxSpan{match.Start, match.End})
		}
	}
	slices.SortFunc(spans, func(a, b spdxSpan) int { return cmp.Compare(a.start, b.start) })
	return spans
}

func removeMatches(result *Result, remove func(Match) bool) bool {
	changed := false
	for index := range result.Detections {
		detection := &result.Detections[index]
		before := len(detection.Matches)
		detection.Matches = slices.DeleteFunc(detection.Matches, remove)
		changed = changed || before != len(detection.Matches)
	}
	result.Detections = slices.DeleteFunc(result.Detections, func(detection Detection) bool {
		return len(detection.Matches) == 0
	})
	before := len(result.Clues)
	result.Clues = slices.DeleteFunc(result.Clues, remove)
	return changed || before != len(result.Clues)
}

// indexSPDXAnchor returns the next offset at which the four bytes SPDX begin,
// case-insensitively, or -1.
func indexSPDXAnchor(input []byte, from int) int {
	for offset := from; offset+4 <= len(input); {
		relative := bytes.IndexAny(input[offset:], "sS")
		if relative < 0 {
			return -1
		}
		anchor := offset + relative
		if anchor+4 <= len(input) &&
			lowerASCII(input[anchor+1]) == 'p' &&
			lowerASCII(input[anchor+2]) == 'd' &&
			lowerASCII(input[anchor+3]) == 'x' {
			return anchor
		}
		offset = anchor + 1
	}
	return -1
}

// spdxTagEnd returns the offset immediately after the tag colon when input at
// anchor begins an SPDX-License-Identifier tag, or -1.
func spdxTagEnd(input []byte, anchor int) int {
	for _, tag := range spdxTags {
		end := anchor + len(tag)
		if end < len(input) &&
			equalFoldASCII(input[anchor:end], tag) &&
			input[end] == ':' {
			return end + 1
		}
	}
	return -1
}

// spdxExpressionSpan returns the trimmed byte range of the expression that
// follows a tag colon. The expression ends at end-of-line, a closing block
// comment marker, or maxSPDXExpressionBytes.
func spdxExpressionSpan(input []byte, from int) (int, int) {
	limit := min(len(input), from+maxSPDXExpressionBytes)
	end := limit
	for offset := from; offset < limit; offset++ {
		switch input[offset] {
		case '\n', '\r':
			end = offset
		case '*':
			if offset+1 < limit && input[offset+1] == '/' {
				end = offset
			}
		case '-':
			if offset+2 < limit && input[offset+1] == '-' && input[offset+2] == '>' {
				end = offset
			}
		}
		if end != limit {
			break
		}
	}
	start := from
	for start < end && input[start] == ' ' || start < end && input[start] == '\t' {
		start++
	}
	for end > start && (input[end-1] == ' ' || input[end-1] == '\t') {
		end--
	}
	return start, end
}

// normalizeExpression parses raw SPDX expression bytes and returns the
// expression rewritten with canonical SPDX identifiers, along with its public
// identifiers and the ScanCode keys used to classify the result.
func (index spdxIndex) normalizeExpression(raw []byte) (string, []string, []string) {
	expression, err := spdx.ParseSyntax(string(raw))
	if err != nil {
		return "", nil, nil
	}
	foldSPDXPlusModifiers(expression)

	var identifiers, scanCodeIDs []string
	valid := true
	rewritten := spdx.RewriteIdentifiers(expression, func(identifier string) string {
		key, ok := index.resolve(identifier)
		if !ok {
			valid = false
			return identifier
		}
		if !slices.Contains(scanCodeIDs, key) {
			scanCodeIDs = append(scanCodeIDs, key)
		}
		reportingID := index.report(key)
		if !slices.Contains(identifiers, reportingID) {
			identifiers = append(identifiers, reportingID)
		}
		return reportingID
	})
	if !valid || len(identifiers) == 0 {
		return "", nil, nil
	}
	return rewritten, identifiers, scanCodeIDs
}

// foldSPDXPlusModifiers lets the corpus resolve deprecated SPDX aliases such
// as GPL-2.0+ as a whole. RewriteIdentifiers otherwise preserves + as syntax
// and presents GPL-2.0 alone to the callback.
func foldSPDXPlusModifiers(expression spdx.Expression) {
	switch node := expression.(type) {
	case *spdx.License:
		if node.Plus {
			node.ID += "+"
			node.Plus = false
		}
	case *spdx.AndExpression:
		foldSPDXPlusModifiers(node.Left)
		foldSPDXPlusModifiers(node.Right)
	case *spdx.OrExpression:
		foldSPDXPlusModifiers(node.Left)
		foldSPDXPlusModifiers(node.Right)
	}
}

func equalFoldASCII(input []byte, lower string) bool {
	if len(input) != len(lower) {
		return false
	}
	for index := range lower {
		if lowerASCII(input[index]) != lower[index] {
			return false
		}
	}
	return true
}

func lowerASCII(character byte) byte {
	if character >= 'A' && character <= 'Z' {
		return character + 'a' - 'A'
	}
	return character
}
