package corpus

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/git-pkgs/licenses/internal/aho"
)

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	automaton, err := aho.Build([]aho.Pattern{
		{Tokens: []uint32{1, 2}, Value: 0},
		{Tokens: []uint32{3, 4}, Value: 1},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{
		Info: Info{
			Version:      "test-version",
			RuleCount:    2,
			SourceCommit: "0123456789abcdef",
		},
		Vocabulary:  []string{"apache", "license", "permission", "zlib"},
		StopwordIDs: []uint32{2},
		Rules: []Rule{
			{
				ID:            "a.LICENSE",
				Expression:    "apache-2.0",
				Tokens:        []uint32{1, 2},
				StopwordAfter: []uint32{1, 1},
				Flags:         FlagLicenseText,
				Relevance:     100,
			},
			{
				ID:         "z.RULE",
				Expression: "zlib",
				Tokens:     []uint32{3, 4},
				Flags:      FlagLicenseNotice | FlagContinuous,
				Relevance:  90,
			},
		},
		Automaton: automaton,
		SPDXKeys: map[string]string{
			"apache-2.0":   "apache-2.0",
			"bsd-3-clause": "bsd-new",
		},
		ReportingIDs: map[string]string{
			"apache-2.0": "Apache-2.0",
			"bsd-new":    "BSD-3-Clause",
		},
	}

	var first bytes.Buffer
	if err := Write(&first, index); err != nil {
		t.Fatal(err)
	}
	var second bytes.Buffer
	if err := Write(&second, index); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("encoding is not deterministic")
	}

	got, err := Read(bytes.NewReader(first.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got.Info.RuleCount != 2 {
		t.Fatalf("rule count = %d, want 2", got.Info.RuleCount)
	}
	if got.Rules[0].ID != "a.LICENSE" || got.Rules[1].ID != "z.RULE" {
		t.Fatalf("rules not sorted: %q, %q", got.Rules[0].ID, got.Rules[1].ID)
	}
	z := got.Rules[1]
	if z.Expression != "zlib" || z.Relevance != 90 {
		t.Fatalf("decoded rule differs: %#v", z)
	}
	if !slices.Equal(z.Tokens, []uint32{3, 4}) {
		t.Fatalf("tokens = %#v", z.Tokens)
	}
	if !slices.Equal(got.Rules[0].StopwordAfter, []uint32{1, 1}) {
		t.Fatalf("rule stopword positions = %#v", got.Rules[0].StopwordAfter)
	}
	if !slices.Equal(got.Vocabulary, index.Vocabulary) {
		t.Fatalf("vocabulary = %#v", got.Vocabulary)
	}
	if !slices.Equal(got.StopwordIDs, index.StopwordIDs) {
		t.Fatalf("stopword IDs = %#v", got.StopwordIDs)
	}
	if err := got.Automaton.Validate(2); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Automaton.EdgeStarts, index.Automaton.EdgeStarts) {
		t.Fatalf("edge starts = %#v, want %#v", got.Automaton.EdgeStarts, index.Automaton.EdgeStarts)
	}
	if !slices.Equal(got.Automaton.EdgeTokens, index.Automaton.EdgeTokens) {
		t.Fatalf("edge tokens = %#v, want %#v", got.Automaton.EdgeTokens, index.Automaton.EdgeTokens)
	}
	if !slices.Equal(got.Automaton.TerminalHeads, index.Automaton.TerminalHeads) {
		t.Fatalf("terminal heads = %#v, want %#v", got.Automaton.TerminalHeads, index.Automaton.TerminalHeads)
	}
	if len(got.SPDXKeys) != 2 ||
		got.SPDXKeys["apache-2.0"] != "apache-2.0" ||
		got.SPDXKeys["bsd-3-clause"] != "bsd-new" {
		t.Fatalf("spdx keys = %#v", got.SPDXKeys)
	}
	if len(got.ReportingIDs) != 2 ||
		got.ReportingIDs["apache-2.0"] != "Apache-2.0" ||
		got.ReportingIDs["bsd-new"] != "BSD-3-Clause" {
		t.Fatalf("reporting IDs = %#v", got.ReportingIDs)
	}
}

func TestRuleFlagValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		got  uint16
		want uint16
	}{
		{name: "license text", got: FlagLicenseText, want: 2},
		{name: "license notice", got: FlagLicenseNotice, want: 4},
		{name: "license tag", got: FlagLicenseTag, want: 8},
		{name: "license reference", got: FlagLicenseReference, want: 16},
		{name: "license intro", got: FlagLicenseIntro, want: 32},
		{name: "license clue", got: FlagLicenseClue, want: 64},
		{name: "false positive", got: FlagFalsePositive, want: 128},
		{name: "required phrase", got: FlagRequiredPhrase, want: 256},
		{name: "continuous", got: FlagContinuous, want: 512},
		{name: "deprecated", got: FlagDeprecated, want: 1024},
	}
	for _, test := range tests {
		if test.got != test.want {
			t.Errorf("%s flag = %d, want %d", test.name, test.got, test.want)
		}
	}
}

func TestWriteRejectsDuplicateRuleIDs(t *testing.T) {
	t.Parallel()

	index := Index{
		Info: Info{Version: "test", SourceCommit: "commit"},
		Rules: []Rule{
			{ID: "same.RULE", Expression: "mit"},
			{ID: "same.RULE", Expression: "apache-2.0"},
		},
	}
	if err := Write(&bytes.Buffer{}, index); err == nil {
		t.Fatal("Write accepted duplicate rule IDs")
	}
}

func TestWriteRejectsWrongRuleCount(t *testing.T) {
	t.Parallel()

	index := Index{
		Info:  Info{Version: "test", SourceCommit: "commit", RuleCount: 2},
		Rules: []Rule{{ID: "one.RULE", Expression: "mit"}},
	}
	if err := Write(&bytes.Buffer{}, index); err == nil {
		t.Fatal("Write accepted an incorrect rule count")
	}
}

func TestWriteRejectsInvalidRuleStopwordPosition(t *testing.T) {
	t.Parallel()

	index := Index{
		Info:       Info{Version: "test", SourceCommit: "commit"},
		Vocabulary: []string{"first", "second"},
		Rules: []Rule{{
			ID:            "one.RULE",
			Expression:    "mit",
			Tokens:        []uint32{1, 2},
			StopwordAfter: []uint32{2},
		}},
	}
	if err := Write(&bytes.Buffer{}, index); err == nil {
		t.Fatal("Write accepted a trailing rule stopword")
	}
}

func TestReadRejectsInvalidData(t *testing.T) {
	t.Parallel()

	if _, err := Read(bytes.NewReader([]byte("not a corpus"))); err == nil {
		t.Fatal("Read accepted invalid data")
	}
}

func TestEdgeCountsRoundTrip(t *testing.T) {
	t.Parallel()

	want := []uint32{0, 2, 2, 2}
	var encoded bytes.Buffer
	if err := writeEdgeCounts(&encoded, want); err != nil {
		t.Fatal(err)
	}
	got, err := readEdgeCounts(bufio.NewReader(&encoded), len(want)-1)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("edge starts = %#v, want %#v", got, want)
	}
}

func TestReadEdgeCountsRejectsNonZeroPadding(t *testing.T) {
	t.Parallel()

	encoded := bytes.NewReader([]byte{0b10000011})
	if _, err := readEdgeCounts(bufio.NewReader(encoded), 3); err == nil {
		t.Fatal("readEdgeCounts accepted non-zero padding")
	}
}

func TestEdgeTokensRoundTrip(t *testing.T) {
	t.Parallel()

	edgeStarts := []uint32{0, 2, 2}
	for _, want := range [][]uint32{
		{1, 1_000},
		{1, 70_000},
	} {
		var encoded bytes.Buffer
		if err := writeEdgeTokens(&encoded, edgeStarts, want); err != nil {
			t.Fatal(err)
		}
		got, err := readEdgeTokens(bufio.NewReader(&encoded), edgeStarts)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("edge tokens = %#v, want %#v", got, want)
		}
	}
}

func TestReadEdgeTokensRejectsInvalidWidth(t *testing.T) {
	t.Parallel()

	var encoded bytes.Buffer
	if err := writeUvarint(&encoded, bitsPerByte); err != nil {
		t.Fatal(err)
	}
	if _, err := readEdgeTokens(bufio.NewReader(&encoded), []uint32{0, 1}); err == nil {
		t.Fatal("readEdgeTokens accepted an invalid width")
	}
}

func TestReadEdgeTokensRejectsZeroDelta(t *testing.T) {
	t.Parallel()

	var encoded bytes.Buffer
	if err := writeUvarint(&encoded, shortTokenBit); err != nil {
		t.Fatal(err)
	}
	encoded.Write([]byte{0, 0})
	if _, err := readEdgeTokens(bufio.NewReader(&encoded), []uint32{0, 1}); err == nil {
		t.Fatal("readEdgeTokens accepted a zero delta")
	}
}

func TestTerminalHeadsRoundTrip(t *testing.T) {
	t.Parallel()

	want := []uint32{aho.None, 2, aho.None, 0, aho.None, 1}
	var encoded bytes.Buffer
	if err := writeTerminalHeads(&encoded, want); err != nil {
		t.Fatal(err)
	}
	got, err := readTerminalHeads(bufio.NewReader(&encoded), len(want))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("terminal heads = %#v, want %#v", got, want)
	}
}

func TestReadTerminalHeadsRejectsDuplicateNode(t *testing.T) {
	t.Parallel()

	var encoded bytes.Buffer
	for _, value := range []uint64{2, 1, 0, 0, 1} {
		if err := writeUvarint(&encoded, value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := readTerminalHeads(bufio.NewReader(&encoded), 2); err == nil {
		t.Fatal("readTerminalHeads accepted a duplicate node")
	}
}

func automatonTestIndex(t *testing.T) Index {
	t.Helper()

	rules := []Rule{
		{ID: "a.LICENSE", Expression: "apache-2.0", Tokens: []uint32{1, 2, 3}, Flags: FlagLicenseText, Relevance: 100},
		{ID: "b.RULE", Expression: "apache-2.0", Tokens: []uint32{1, 2}, Flags: FlagLicenseNotice, Relevance: 100},
		{ID: "c.RULE", Expression: "mit", Flags: FlagLicenseTag, Relevance: 90},
		{ID: "d.RULE", Expression: "zlib", Tokens: []uint32{2, 3}, Flags: FlagLicenseReference, Relevance: 80},
	}
	patterns := make([]aho.Pattern, 0, len(rules))
	for index, rule := range rules {
		patterns = append(patterns, aho.Pattern{Tokens: rule.Tokens, Value: uint32(index)})
	}
	automaton, err := aho.Build(patterns, len(rules))
	if err != nil {
		t.Fatal(err)
	}
	return Index{
		Info:         Info{Version: "test-version", RuleCount: len(rules), SourceCommit: "0123456789abcdef"},
		Vocabulary:   []string{"apache", "license", "version"},
		StopwordIDs:  []uint32{2},
		Rules:        rules,
		Automaton:    automaton,
		SPDXKeys:     map[string]string{"apache-2.0": "apache-2.0", "mit": "mit"},
		ReportingIDs: map[string]string{"apache-2.0": "Apache-2.0", "mit": "MIT"},
	}
}

func encodeIndex(t *testing.T, index Index, version uint64, storeAutomaton bool) []byte {
	t.Helper()

	var data bytes.Buffer
	if err := encode(&data, index, version, storeAutomaton); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func decompress(t *testing.T, data []byte) []byte {
	t.Helper()

	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func compress(t *testing.T, raw []byte) []byte {
	t.Helper()

	var data bytes.Buffer
	zw := gzip.NewWriter(&data)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestWriteWithoutAutomatonRebuildsOnRead(t *testing.T) {
	t.Parallel()

	index := automatonTestIndex(t)
	var stored, omitted, repeated bytes.Buffer
	if err := Write(&stored, index); err != nil {
		t.Fatal(err)
	}
	if err := WriteWithoutAutomaton(&omitted, index); err != nil {
		t.Fatal(err)
	}
	if err := WriteWithoutAutomaton(&repeated, index); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(omitted.Bytes(), repeated.Bytes()) {
		t.Fatal("encoding without the automaton is not deterministic")
	}
	if len(decompress(t, omitted.Bytes())) >= len(decompress(t, stored.Bytes())) {
		t.Fatal("omitting the automaton did not shrink the index")
	}

	want, err := Read(bytes.NewReader(stored.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Read(bytes.NewReader(omitted.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("index read without a stored automaton differs from the stored index")
	}
	if !reflect.DeepEqual(got.Automaton, index.Automaton) {
		t.Fatal("rebuilt automaton differs from the built automaton")
	}
}

func TestWriteWithoutAutomatonSkipsAutomatonValidation(t *testing.T) {
	t.Parallel()

	index := automatonTestIndex(t)
	index.Automaton = aho.Automaton{}
	if err := Write(&bytes.Buffer{}, index); err == nil {
		t.Fatal("Write accepted a missing automaton")
	}
	var data bytes.Buffer
	if err := WriteWithoutAutomaton(&data, index); err != nil {
		t.Fatal(err)
	}
	got, err := Read(&data)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Automaton.Validate(len(got.Rules)); err != nil {
		t.Fatal(err)
	}
}

func TestReadLegacyFormat(t *testing.T) {
	t.Parallel()

	index := automatonTestIndex(t)
	legacy := encodeIndex(t, index, legacyFormatVersion, true)
	current := encodeIndex(t, index, FormatVersion, true)
	if raw := decompress(t, legacy); raw[len(magic)] != legacyFormatVersion {
		t.Fatalf("legacy format version = %d", raw[len(magic)])
	}

	want, err := Read(bytes.NewReader(current))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Read(bytes.NewReader(legacy))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("legacy index differs from the current index")
	}
}

func TestEncodeRejectsLegacyFormatWithoutAutomaton(t *testing.T) {
	t.Parallel()

	err := encode(&bytes.Buffer{}, automatonTestIndex(t), legacyFormatVersion, false)
	if err == nil || !strings.Contains(err.Error(), "requires the automaton") {
		t.Fatalf("error = %v", err)
	}
}

func TestReadRejectsInvalidAutomatonMarker(t *testing.T) {
	t.Parallel()

	index := automatonTestIndex(t)
	stored := decompress(t, encodeIndex(t, index, FormatVersion, true))
	omitted := decompress(t, encodeIndex(t, index, FormatVersion, false))
	marker := 0
	for stored[marker] == omitted[marker] {
		marker++
	}
	if stored[marker] != automatonStored || omitted[marker] != automatonOmitted {
		t.Fatalf("markers = %d, %d", stored[marker], omitted[marker])
	}

	omitted[marker] = 2
	_, err := Read(bytes.NewReader(compress(t, omitted)))
	if err == nil || !strings.Contains(err.Error(), "invalid automaton marker 2") {
		t.Fatalf("error = %v", err)
	}
	_, err = Read(bytes.NewReader(compress(t, omitted[:marker])))
	if err == nil || !strings.Contains(err.Error(), "read automaton marker") {
		t.Fatalf("truncated marker error = %v", err)
	}
}

func TestReadRejectsUnsupportedFormatVersions(t *testing.T) {
	t.Parallel()

	raw := decompress(t, encodeIndex(t, automatonTestIndex(t), FormatVersion, true))
	for _, version := range []byte{legacyFormatVersion - 1, FormatVersion + 1} {
		patched := slices.Clone(raw)
		patched[len(magic)] = version
		_, err := Read(bytes.NewReader(compress(t, patched)))
		want := fmt.Sprintf("unsupported format version %d", version)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("version %d error = %v", version, err)
		}
	}
}

// rebuildLimitIndex returns an index whose rules hold tokenCount tokens in
// total. The rules share one token slice to keep the test small.
func rebuildLimitIndex(tokenCount int) Index {
	shared := make([]uint32, maxTokenCount)
	for position := range shared {
		shared[position] = 1
	}
	var rules []Rule
	for remaining := tokenCount; remaining > 0; remaining -= maxTokenCount {
		rules = append(rules, Rule{
			ID:         fmt.Sprintf("rule-%02d.RULE", len(rules)),
			Expression: "mit",
			Tokens:     shared[:min(remaining, maxTokenCount)],
		})
	}
	return Index{
		Info:       Info{Version: "test", RuleCount: len(rules), SourceCommit: "commit"},
		Vocabulary: []string{"license"},
		Rules:      rules,
	}
}

func TestRebuildPatternsHoldsTokensBelowNodeLimit(t *testing.T) {
	t.Parallel()

	patterns, err := rebuildPatterns(rebuildLimitIndex(maxNodeCount - 1).Rules)
	if err != nil {
		t.Fatal(err)
	}
	if len(patterns) != 10 {
		t.Fatalf("patterns = %d, want 10", len(patterns))
	}
	_, err = rebuildPatterns(rebuildLimitIndex(maxNodeCount).Rules)
	if err == nil || !strings.Contains(err.Error(), "automaton rebuild limit") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteWithoutAutomatonRejectsUnloadableIndex(t *testing.T) {
	t.Parallel()

	var data bytes.Buffer
	err := WriteWithoutAutomaton(&data, rebuildLimitIndex(maxNodeCount))
	if err == nil || !strings.Contains(err.Error(), "automaton rebuild limit") {
		t.Fatalf("error = %v", err)
	}
	if data.Len() != 0 {
		t.Fatalf("wrote %d bytes for a rejected index", data.Len())
	}
}

func TestReadRejectsOmittedAutomatonOverRebuildLimit(t *testing.T) {
	t.Parallel()

	index := rebuildLimitIndex(maxNodeCount)
	var raw bytes.Buffer
	bw := bufio.NewWriter(&raw)
	if err := writeHeader(bw, FormatVersion, index.Info); err != nil {
		t.Fatal(err)
	}
	if err := writeStrings(bw, index.Vocabulary); err != nil {
		t.Fatal(err)
	}
	if err := writeUint32s(bw, index.StopwordIDs); err != nil {
		t.Fatal(err)
	}
	if err := writeUvarint(bw, uint64(len(index.Rules))); err != nil {
		t.Fatal(err)
	}
	for _, rule := range index.Rules {
		if err := writeRule(bw, rule); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeAutomatonSection(bw, FormatVersion, aho.Automaton{}, false); err != nil {
		t.Fatal(err)
	}
	if err := writeStringMap(bw, nil); err != nil {
		t.Fatal(err)
	}
	if err := writeStringMap(bw, nil); err != nil {
		t.Fatal(err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatal(err)
	}

	_, err := Read(bytes.NewReader(compress(t, raw.Bytes())))
	if err == nil || !strings.Contains(err.Error(), "automaton rebuild limit") {
		t.Fatalf("error = %v", err)
	}
}
