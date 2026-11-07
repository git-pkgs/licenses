package corpus

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"sort"
	"time"

	"github.com/git-pkgs/licenses/internal/aho"
)

// FormatVersion is the on-disk corpus index format. Version 8 records whether
// the automaton is stored or must be rebuilt from the rule tokens.
const FormatVersion = 8

// legacyFormatVersion always stores the automaton and remains readable.
const legacyFormatVersion = 7

const (
	automatonOmitted byte = 0
	automatonStored  byte = 1
)

const (
	FlagLicenseText      uint16 = 1 << 1
	FlagLicenseNotice    uint16 = 1 << 2
	FlagLicenseTag       uint16 = 1 << 3
	FlagLicenseReference uint16 = 1 << 4
	FlagLicenseIntro     uint16 = 1 << 5
	FlagLicenseClue      uint16 = 1 << 6
	FlagFalsePositive    uint16 = 1 << 7
	FlagRequiredPhrase   uint16 = 1 << 8
	FlagContinuous       uint16 = 1 << 9
	FlagDeprecated       uint16 = 1 << 10
)

const (
	maxRuleCount  = 100_000
	maxWordCount  = 1_000_000
	maxTokenCount = 1_000_000
	maxNodeCount  = 10_000_000
	maxStringLen  = 16 << 20
	bufferSize    = 256 << 10
	bitsPerByte   = 8
	trieBitFactor = 2
	shortTokenBit = 16
	fullTokenBit  = 32
	unknownOS     = 255
)

var magic = [8]byte{'G', 'L', 'I', 'C', 'I', 'D', 'X', 0}

// Info identifies the source and contents of an index.
type Info struct {
	Version      string
	RuleCount    int
	SourceCommit string
}

// Rule is the source data needed to build the matching indexes.
type Rule struct {
	ID            string
	Expression    string
	Text          []byte
	Tokens        []uint32
	TokenCount    uint32
	TokenHash     uint64
	StopwordAfter []uint32
	Flags         uint16
	Relevance     uint8
}

// Index is a decoded corpus.
type Index struct {
	Info        Info
	Vocabulary  []string
	StopwordIDs []uint32
	Rules       []Rule
	Automaton   aho.Automaton
	// SPDXKeys maps lowercase SPDX identifiers, and their deprecated aliases,
	// to ScanCode license keys.
	SPDXKeys map[string]string
	// ReportingIDs maps ScanCode license keys to their canonical SPDX
	// identifier or LicenseRef-scancode-* value.
	ReportingIDs map[string]string
}

// Write encodes index as a deterministic gzip-compressed binary stream.
func Write(w io.Writer, index Index) error {
	return encode(w, index, FormatVersion, true)
}

// WriteWithoutAutomaton encodes index like Write but leaves out the
// automaton, which Read rebuilds from the rule tokens. The output is smaller
// and slower to load.
func WriteWithoutAutomaton(w io.Writer, index Index) error {
	return encode(w, index, FormatVersion, false)
}

func encode(w io.Writer, index Index, version uint64, storeAutomaton bool) error {
	if err := validateIndex(index, storeAutomaton); err != nil {
		return err
	}
	if !storeAutomaton {
		// Refuse output that Read could not rebuild.
		if _, err := rebuildPatterns(index.Rules); err != nil {
			return err
		}
	}

	zw, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("corpus: create compressor: %w", err)
	}
	zw.ModTime = time.Unix(0, 0).UTC()
	zw.OS = unknownOS
	bw := bufio.NewWriterSize(zw, bufferSize)

	if err := writeHeader(bw, version, index.Info); err != nil {
		_ = zw.Close()
		return err
	}
	if err := writeStrings(bw, index.Vocabulary); err != nil {
		_ = zw.Close()
		return err
	}
	if err := writeUint32s(bw, index.StopwordIDs); err != nil {
		_ = zw.Close()
		return err
	}
	if err := writeUvarint(bw, uint64(len(index.Rules))); err != nil {
		_ = zw.Close()
		return err
	}
	for _, rule := range index.Rules {
		if err := writeRule(bw, rule); err != nil {
			_ = zw.Close()
			return err
		}
	}
	if err := writeAutomatonSection(bw, version, index.Automaton, storeAutomaton); err != nil {
		_ = zw.Close()
		return err
	}
	if err := writeStringMap(bw, index.SPDXKeys); err != nil {
		_ = zw.Close()
		return err
	}
	if err := writeStringMap(bw, index.ReportingIDs); err != nil {
		_ = zw.Close()
		return err
	}
	if err := bw.Flush(); err != nil {
		_ = zw.Close()
		return fmt.Errorf("corpus: flush index: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("corpus: close compressor: %w", err)
	}
	return nil
}

func validateIndex(index Index, storeAutomaton bool) error {
	if index.Info.Version == "" {
		return errors.New("corpus: missing version")
	}
	if index.Info.SourceCommit == "" {
		return errors.New("corpus: missing source commit")
	}
	if len(index.Rules) > maxRuleCount {
		return fmt.Errorf("corpus: %d rules exceeds limit", len(index.Rules))
	}
	if len(index.Vocabulary) > maxWordCount {
		return fmt.Errorf("corpus: %d words exceeds limit", len(index.Vocabulary))
	}
	if index.Info.RuleCount != 0 && index.Info.RuleCount != len(index.Rules) {
		return fmt.Errorf(
			"corpus: metadata reports %d rules, index contains %d",
			index.Info.RuleCount,
			len(index.Rules),
		)
	}
	for wordIndex, word := range index.Vocabulary {
		if word == "" {
			return fmt.Errorf("corpus: vocabulary word %d is empty", wordIndex)
		}
		if wordIndex > 0 && index.Vocabulary[wordIndex-1] >= word {
			return fmt.Errorf("corpus: vocabulary is not strictly sorted at %q", word)
		}
	}
	var previousStopword uint32
	for position, stopword := range index.StopwordIDs {
		if stopword == 0 || uint64(stopword) > uint64(len(index.Vocabulary)) {
			return fmt.Errorf("corpus: invalid stopword token %d", stopword)
		}
		if position > 0 && stopword <= previousStopword {
			return fmt.Errorf("corpus: stopword tokens are not strictly sorted at %d", stopword)
		}
		previousStopword = stopword
	}

	rules := index.Rules
	for i, rule := range rules {
		if rule.ID == "" {
			return fmt.Errorf("corpus: rule %d has no ID", i)
		}
		if rule.Expression == "" && rule.Flags&FlagFalsePositive == 0 {
			return fmt.Errorf("corpus: rule %q has no expression", rule.ID)
		}
		if i > 0 && rules[i-1].ID >= rule.ID {
			return fmt.Errorf("corpus: rules are not strictly sorted at %q", rule.ID)
		}
		if len(rule.Tokens) > maxTokenCount {
			return fmt.Errorf("corpus: rule %q has too many tokens", rule.ID)
		}
		for _, token := range rule.Tokens {
			if token == 0 || uint64(token) > uint64(len(index.Vocabulary)) {
				return fmt.Errorf("corpus: rule %q has invalid token %d", rule.ID, token)
			}
		}
		var previous uint32
		for position, after := range rule.StopwordAfter {
			if after == 0 || uint64(after) >= uint64(len(rule.Tokens)) {
				return fmt.Errorf("corpus: rule %q has invalid stopword position %d", rule.ID, after)
			}
			if position > 0 && after < previous {
				return fmt.Errorf("corpus: rule %q stopword positions are not sorted at %d", rule.ID, after)
			}
			previous = after
		}
	}
	if storeAutomaton {
		if err := index.Automaton.Validate(len(rules)); err != nil {
			return fmt.Errorf("corpus: invalid automaton: %w", err)
		}
	}
	return validateIdentifierMaps(index)
}

func validateIdentifierMaps(index Index) error {
	if err := validateIdentifierMap("SPDX keys", index.SPDXKeys); err != nil {
		return err
	}
	return validateIdentifierMap("reporting IDs", index.ReportingIDs)
}

func validateIdentifierMap(name string, keys map[string]string) error {
	if len(keys) > maxRuleCount {
		return fmt.Errorf("corpus: %d %s exceeds limit", len(keys), name)
	}
	for key, value := range keys {
		if key == "" || value == "" {
			return fmt.Errorf("corpus: %s %q maps to %q", name, key, value)
		}
	}
	return nil
}

func writeHeader(w io.Writer, version uint64, info Info) error {
	if _, err := w.Write(magic[:]); err != nil {
		return fmt.Errorf("corpus: write magic: %w", err)
	}
	if err := writeUvarint(w, version); err != nil {
		return err
	}
	if err := writeString(w, info.Version); err != nil {
		return err
	}
	return writeString(w, info.SourceCommit)
}

func writeRule(w io.Writer, rule Rule) error {
	if err := writeString(w, rule.ID); err != nil {
		return err
	}
	if err := writeString(w, rule.Expression); err != nil {
		return err
	}
	var metadata [3]byte
	binary.LittleEndian.PutUint16(metadata[:2], rule.Flags)
	metadata[2] = rule.Relevance
	if _, err := w.Write(metadata[:]); err != nil {
		return fmt.Errorf("corpus: write metadata for %q: %w", rule.ID, err)
	}
	if err := writeUint32s(w, rule.Tokens); err != nil {
		return err
	}
	return writeUint32s(w, rule.StopwordAfter)
}

func writeUint32s(w io.Writer, values []uint32) error {
	if err := writeUvarint(w, uint64(len(values))); err != nil {
		return err
	}
	for _, value := range values {
		if err := writeUvarint(w, uint64(value)); err != nil {
			return err
		}
	}
	return nil
}

func writeAutomatonSection(w io.Writer, version uint64, automaton aho.Automaton, store bool) error {
	if version != legacyFormatVersion {
		marker := automatonOmitted
		if store {
			marker = automatonStored
		}
		if _, err := w.Write([]byte{marker}); err != nil {
			return fmt.Errorf("corpus: write automaton marker: %w", err)
		}
	}
	if !store {
		return nil
	}
	return writeAutomaton(w, automaton)
}

func writeAutomaton(w io.Writer, automaton aho.Automaton) error {
	nodeCount := automaton.NodeCount()
	edgeStarts := automaton.DenseEdgeStarts()
	if err := writeUvarint(w, uint64(nodeCount)); err != nil {
		return err
	}
	if err := writeEdgeCounts(w, edgeStarts); err != nil {
		return err
	}
	if err := writeTerminalHeads(w, automaton.Outputs); err != nil {
		return err
	}
	if err := writeEdgeTokens(w, edgeStarts, automaton.DenseEdgeTokens()); err != nil {
		return err
	}
	for _, next := range automaton.OutputNext {
		if err := writeOptional(w, next); err != nil {
			return err
		}
	}
	return nil
}

func writeEdgeTokens(w io.Writer, edgeStarts, edgeTokens []uint32) error {
	var maxDelta uint32
	for node := 0; node+1 < len(edgeStarts); node++ {
		var previous uint32
		for edge := edgeStarts[node]; edge < edgeStarts[node+1]; edge++ {
			delta := edgeTokens[edge] - previous
			maxDelta = max(maxDelta, delta)
			previous = edgeTokens[edge]
		}
	}
	width := bits.Len32(maxDelta)
	if width > 0 && width <= shortTokenBit {
		width = shortTokenBit
	} else if width > shortTokenBit {
		width = fullTokenBit
	}
	if err := writeUvarint(w, uint64(width)); err != nil {
		return err
	}
	byteWidth := width / bitsPerByte
	encoded := make([]byte, len(edgeTokens)*byteWidth)
	position := 0
	for node := 0; node+1 < len(edgeStarts); node++ {
		var previous uint32
		for edge := edgeStarts[node]; edge < edgeStarts[node+1]; edge++ {
			delta := edgeTokens[edge] - previous
			switch width {
			case shortTokenBit:
				binary.LittleEndian.PutUint16(encoded[position:], uint16(delta))
			case fullTokenBit:
				binary.LittleEndian.PutUint32(encoded[position:], delta)
			}
			position += byteWidth
			previous = edgeTokens[edge]
		}
	}
	if _, err := w.Write(encoded); err != nil {
		return fmt.Errorf("corpus: write automaton edge tokens: %w", err)
	}
	return nil
}

func writeEdgeCounts(w io.Writer, edgeStarts []uint32) error {
	nodeCount := len(edgeStarts) - 1
	bitCount := trieBitFactor*nodeCount - 1
	encoded := make([]byte, (bitCount+bitsPerByte-1)/bitsPerByte)
	position := 0
	for node := range nodeCount {
		edgeCount := int(edgeStarts[node+1] - edgeStarts[node])
		for range edgeCount {
			encoded[position/bitsPerByte] |= 1 << (position % bitsPerByte)
			position++
		}
		position++
	}
	if _, err := w.Write(encoded); err != nil {
		return fmt.Errorf("corpus: write automaton edge counts: %w", err)
	}
	return nil
}

func writeTerminalHeads(w io.Writer, outputs aho.SparseOutputs) error {
	if err := writeUvarint(w, uint64(outputs.TerminalCount())); err != nil {
		return err
	}
	var previous uint64
	var writeErr error
	outputs.RangeTerminalHeads(func(node, head uint32) {
		if writeErr != nil {
			return
		}
		current := uint64(node) + 1
		if err := writeUvarint(w, current-previous); err != nil {
			writeErr = err
			return
		}
		if err := writeUvarint(w, uint64(head)); err != nil {
			writeErr = err
			return
		}
		previous = current
	})
	return writeErr
}

func writeOptional(w io.Writer, value uint32) error {
	if value == aho.None {
		return writeUvarint(w, 0)
	}
	return writeUvarint(w, uint64(value)+1)
}

func writeStringMap(w io.Writer, values map[string]string) error {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if err := writeUvarint(w, uint64(len(keys))); err != nil {
		return err
	}
	for _, key := range keys {
		if err := writeString(w, key); err != nil {
			return err
		}
		if err := writeString(w, values[key]); err != nil {
			return err
		}
	}
	return nil
}

func writeStrings(w io.Writer, values []string) error {
	if err := writeUvarint(w, uint64(len(values))); err != nil {
		return err
	}
	for _, value := range values {
		if err := writeString(w, value); err != nil {
			return err
		}
	}
	return nil
}

func writeString(w io.Writer, value string) error {
	return writeBytes(w, []byte(value))
}

func writeBytes(w io.Writer, value []byte) error {
	if err := writeUvarint(w, uint64(len(value))); err != nil {
		return err
	}
	if _, err := w.Write(value); err != nil {
		return fmt.Errorf("corpus: write value: %w", err)
	}
	return nil
}

func writeUvarint(w io.Writer, value uint64) error {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], value)
	if _, err := w.Write(buf[:n]); err != nil {
		return fmt.Errorf("corpus: write integer: %w", err)
	}
	return nil
}

// Read decodes a corpus index and checks its framing and limits.
func Read(r io.Reader) (Index, error) {
	return read(r, true)
}

// ReadRuntime decodes an index without retaining tokens already stored in its automaton.
func ReadRuntime(r io.Reader) (Index, error) {
	return read(r, false)
}

func read(r io.Reader, retainRuleTokens bool) (Index, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return Index{}, fmt.Errorf("corpus: open compressed index: %w", err)
	}
	defer func() { _ = zr.Close() }()

	br := bufio.NewReaderSize(zr, bufferSize)
	version, info, err := readHeader(br)
	if err != nil {
		return Index{}, err
	}
	vocabulary, err := readVocabulary(br)
	if err != nil {
		return Index{}, err
	}
	stopwordIDs, err := readStopwordIDs(br, len(vocabulary))
	if err != nil {
		return Index{}, err
	}
	rules, err := readRules(br, len(vocabulary), retainRuleTokens)
	if err != nil {
		return Index{}, err
	}
	compactEdgeTokens := !retainRuleTokens && len(vocabulary) <= int(^uint16(0))
	automaton, err := readAutomatonSection(br, version, rules, retainRuleTokens, compactEdgeTokens)
	if err != nil {
		return Index{}, err
	}
	if !retainRuleTokens && !compactEdgeTokens {
		automaton.CompactNodeIndexes()
	}
	spdxKeys, err := readStringMap(br, maxRuleCount)
	if err != nil {
		return Index{}, fmt.Errorf("corpus: read SPDX keys: %w", err)
	}
	reportingIDs, err := readStringMap(br, maxRuleCount)
	if err != nil {
		return Index{}, fmt.Errorf("corpus: read reporting IDs: %w", err)
	}
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		if err == nil {
			return Index{}, errors.New("corpus: trailing data")
		}
		return Index{}, fmt.Errorf("corpus: finish index: %w", err)
	}
	info.RuleCount = len(rules)
	return Index{
		Info:         info,
		Vocabulary:   vocabulary,
		StopwordIDs:  stopwordIDs,
		Rules:        rules,
		Automaton:    automaton,
		SPDXKeys:     spdxKeys,
		ReportingIDs: reportingIDs,
	}, nil
}

func readVocabulary(r *bufio.Reader) ([]string, error) {
	vocabulary, err := readStrings(r, maxWordCount)
	if err != nil {
		return nil, fmt.Errorf("corpus: read vocabulary: %w", err)
	}
	for index, word := range vocabulary {
		if word == "" {
			return nil, fmt.Errorf("corpus: vocabulary word %d is empty", index)
		}
		if index > 0 && vocabulary[index-1] >= word {
			return nil, fmt.Errorf("corpus: vocabulary is not strictly sorted at %q", word)
		}
	}
	return vocabulary, nil
}

func readStopwordIDs(r *bufio.Reader, vocabularySize int) ([]uint32, error) {
	stopwordIDs, err := readUint32s(r, maxWordCount)
	if err != nil {
		return nil, fmt.Errorf("corpus: read stopword tokens: %w", err)
	}
	var previous uint32
	for index, stopword := range stopwordIDs {
		if stopword == 0 || uint64(stopword) > uint64(vocabularySize) {
			return nil, fmt.Errorf("corpus: invalid stopword token %d", stopword)
		}
		if index > 0 && stopword <= previous {
			return nil, fmt.Errorf("corpus: stopword tokens are not strictly sorted at %d", stopword)
		}
		previous = stopword
	}
	return stopwordIDs, nil
}

func readRules(r *bufio.Reader, vocabularySize int, retainTokens bool) ([]Rule, error) {
	count, err := readCount(r, maxRuleCount, "rule")
	if err != nil {
		return nil, err
	}
	rules := make([]Rule, count)
	for index := range rules {
		rules[index], err = readRule(r, vocabularySize, retainTokens)
		if err != nil {
			return nil, fmt.Errorf("corpus: read rule %d: %w", index, err)
		}
		if index > 0 && rules[index-1].ID >= rules[index].ID {
			return nil, fmt.Errorf("corpus: rules are not strictly sorted at %q", rules[index].ID)
		}
		if err := validateRuleStopwordPositions(rules[index]); err != nil {
			return nil, err
		}
	}
	return rules, nil
}

func validateRuleStopwordPositions(rule Rule) error {
	var previous uint32
	for position, after := range rule.StopwordAfter {
		if after == 0 || after >= rule.TokenCount {
			return fmt.Errorf("corpus: rule %q has invalid stopword position %d", rule.ID, after)
		}
		if position > 0 && after < previous {
			return fmt.Errorf("corpus: rule %q stopword positions are not sorted at %d", rule.ID, after)
		}
		previous = after
	}
	return nil
}

func readStringMap(r *bufio.Reader, limit uint64) (map[string]string, error) {
	count, err := readCount(r, limit, "map")
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, count)
	var previous string
	for range count {
		key, err := readString(r)
		if err != nil {
			return nil, err
		}
		if key == "" {
			return nil, errors.New("empty key")
		}
		if key <= previous {
			return nil, fmt.Errorf("keys are not strictly sorted at %q", key)
		}
		previous = key
		value, err := readString(r)
		if err != nil {
			return nil, err
		}
		if value == "" {
			return nil, fmt.Errorf("key %q has an empty value", key)
		}
		values[key] = value
	}
	return values, nil
}

func readHeader(r *bufio.Reader) (uint64, Info, error) {
	var gotMagic [len(magic)]byte
	if _, err := io.ReadFull(r, gotMagic[:]); err != nil {
		return 0, Info{}, fmt.Errorf("corpus: read magic: %w", err)
	}
	if gotMagic != magic {
		return 0, Info{}, errors.New("corpus: invalid magic")
	}
	version, err := binary.ReadUvarint(r)
	if err != nil {
		return 0, Info{}, fmt.Errorf("corpus: read format version: %w", err)
	}
	if version != FormatVersion && version != legacyFormatVersion {
		return 0, Info{}, fmt.Errorf("corpus: unsupported format version %d", version)
	}
	corpusVersion, err := readString(r)
	if err != nil {
		return 0, Info{}, err
	}
	sourceCommit, err := readString(r)
	if err != nil {
		return 0, Info{}, err
	}
	return version, Info{Version: corpusVersion, SourceCommit: sourceCommit}, nil
}

func readRule(r *bufio.Reader, wordCount int, retainTokens bool) (Rule, error) {
	id, err := readString(r)
	if err != nil {
		return Rule{}, err
	}
	expression, err := readString(r)
	if err != nil {
		return Rule{}, err
	}
	var metadata [3]byte
	if _, err := io.ReadFull(r, metadata[:]); err != nil {
		return Rule{}, fmt.Errorf("read metadata: %w", err)
	}
	tokens, tokenCount, tokenHash, err := readRuleTokens(r, wordCount, retainTokens)
	if err != nil {
		return Rule{}, err
	}
	stopwordAfter, err := readUint32s(r, maxTokenCount)
	if err != nil {
		return Rule{}, err
	}
	return Rule{
		ID:            id,
		Expression:    expression,
		Tokens:        tokens,
		TokenCount:    tokenCount,
		TokenHash:     tokenHash,
		StopwordAfter: stopwordAfter,
		Flags:         binary.LittleEndian.Uint16(metadata[:2]),
		Relevance:     metadata[2],
	}, nil
}

const (
	tokenHashOffset = 14695981039346656037
	tokenHashPrime  = 1099511628211
)

func readRuleTokens(r *bufio.Reader, wordCount int, retain bool) ([]uint32, uint32, uint64, error) {
	count, err := readCount(r, maxTokenCount, "integer list")
	if err != nil {
		return nil, 0, 0, err
	}
	var tokens []uint32
	if retain {
		tokens = make([]uint32, count)
	}
	hash := uint64(tokenHashOffset)
	for index := range count {
		value, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("read integer: %w", err)
		}
		if value == 0 || value > uint64(wordCount) {
			return nil, 0, 0, fmt.Errorf("corpus: invalid rule token %d", value)
		}
		token := uint32(value)
		if retain {
			tokens[index] = token
		}
		hash = addTokenHash(hash, token)
	}
	return tokens, uint32(count), hash, nil
}

func addTokenHash(hash uint64, token uint32) uint64 {
	for range 4 {
		hash ^= uint64(byte(token))
		hash *= tokenHashPrime
		token >>= 8
	}
	return hash
}

func readUint32s(r *bufio.Reader, limit uint64) ([]uint32, error) {
	count, err := readCount(r, limit, "integer list")
	if err != nil {
		return nil, err
	}
	values := make([]uint32, count)
	for index := range values {
		value, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, fmt.Errorf("read integer: %w", err)
		}
		if value > uint64(^uint32(0)) {
			return nil, fmt.Errorf("integer %d exceeds uint32", value)
		}
		values[index] = uint32(value)
	}
	return values, nil
}

func readStrings(r *bufio.Reader, limit uint64) ([]string, error) {
	count, err := readCount(r, limit, "list")
	if err != nil {
		return nil, err
	}
	values := make([]string, count)
	for i := range values {
		values[i], err = readString(r)
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

func readAutomatonSection(
	r *bufio.Reader,
	version uint64,
	rules []Rule,
	retainRuleTokens bool,
	compactEdgeTokens bool,
) (aho.Automaton, error) {
	if version == legacyFormatVersion {
		return readAutomaton(r, len(rules), compactEdgeTokens)
	}
	marker, err := r.ReadByte()
	if err != nil {
		return aho.Automaton{}, fmt.Errorf("corpus: read automaton marker: %w", err)
	}
	switch marker {
	case automatonStored:
		return readAutomaton(r, len(rules), compactEdgeTokens)
	case automatonOmitted:
		if !retainRuleTokens {
			return aho.Automaton{}, errors.New("corpus: runtime index omits its automaton")
		}
		return rebuildAutomaton(rules)
	default:
		return aho.Automaton{}, fmt.Errorf("corpus: invalid automaton marker %d", marker)
	}
}

// rebuildAutomaton constructs the automaton from the rule tokens, matching
// the one corpusgen stores.
func rebuildAutomaton(rules []Rule) (aho.Automaton, error) {
	patterns, err := rebuildPatterns(rules)
	if err != nil {
		return aho.Automaton{}, err
	}
	automaton, err := aho.Build(patterns, len(rules))
	if err != nil {
		return aho.Automaton{}, fmt.Errorf("corpus: rebuild automaton: %w", err)
	}
	if err := automaton.Validate(len(rules)); err != nil {
		return aho.Automaton{}, fmt.Errorf("corpus: invalid automaton: %w", err)
	}
	return automaton, nil
}

// rebuildPatterns returns the automaton patterns for rules. Every node past
// the root consumes at least one token and aho.Build allocates per token, so
// keeping the token total below maxNodeCount holds the rebuild to the node
// limit a stored automaton must meet before anything is allocated.
func rebuildPatterns(rules []Rule) ([]aho.Pattern, error) {
	patterns := make([]aho.Pattern, 0, len(rules))
	tokenCount := 0
	for index, rule := range rules {
		if len(rule.Tokens) == 0 {
			continue
		}
		tokenCount += len(rule.Tokens)
		if tokenCount >= maxNodeCount {
			return nil, fmt.Errorf("corpus: rule tokens exceed the automaton rebuild limit of %d", maxNodeCount-1)
		}
		patterns = append(patterns, aho.Pattern{Tokens: rule.Tokens, Value: uint32(index)})
	}
	return patterns, nil
}

func readAutomaton(r *bufio.Reader, valueCount int, compactEdgeTokens bool) (aho.Automaton, error) {
	nodeCount, err := readCount(r, maxNodeCount, "automaton node")
	if err != nil {
		return aho.Automaton{}, err
	}
	if nodeCount == 0 {
		return aho.Automaton{}, errors.New("corpus: automaton has no root")
	}
	var edgeStarts []uint32
	var compactEdgeStarts aho.CompactEdgeStarts
	if compactEdgeTokens {
		compactEdgeStarts, err = readCompactEdgeStarts(r, nodeCount)
	} else {
		edgeStarts, err = readEdgeCounts(r, nodeCount)
	}
	if err != nil {
		return aho.Automaton{}, err
	}

	terminalHeads, err := readTerminalHeads(r, nodeCount)
	if err != nil {
		return aho.Automaton{}, err
	}

	var edgeTokens []uint32
	var edgeTokens16 []uint16
	if compactEdgeTokens {
		edgeTokens16, err = readEdgeTokensAs[uint16](
			r,
			compactEdgeStarts,
			uint32(^uint16(0)),
		)
	} else {
		edgeTokens, err = readEdgeTokens(r, edgeStarts)
	}
	if err != nil {
		return aho.Automaton{}, err
	}
	outputNext, err := readOptionalUint32s(r, valueCount, "output chain")
	if err != nil {
		return aho.Automaton{}, err
	}
	if compactEdgeTokens {
		automaton, err := aho.RestoreCompact(
			compactEdgeStarts,
			edgeTokens16,
			terminalHeads,
			outputNext,
		)
		if err != nil {
			return aho.Automaton{}, fmt.Errorf("corpus: build automaton links: %w", err)
		}
		if err := automaton.Validate(valueCount); err != nil {
			return aho.Automaton{}, fmt.Errorf("corpus: invalid automaton: %w", err)
		}
		return automaton, nil
	}
	failures, outputLinks, err := aho.BuildFailureLinksSparse(edgeStarts, edgeTokens, terminalHeads)
	if err != nil {
		return aho.Automaton{}, fmt.Errorf("corpus: build automaton links: %w", err)
	}

	automaton := aho.Automaton{
		EdgeStarts: edgeStarts,
		EdgeTokens: edgeTokens,
		Failures:   failures,
		Outputs:    terminalHeads.WithOutputLinks(outputLinks),
		OutputNext: outputNext,
	}
	automaton.BuildRootTable()
	if err := automaton.Validate(valueCount); err != nil {
		return aho.Automaton{}, fmt.Errorf("corpus: invalid automaton: %w", err)
	}
	return automaton, nil
}

func readEdgeTokens(r *bufio.Reader, edgeStarts []uint32) ([]uint32, error) {
	return readEdgeTokensAs[uint32](r, storedEdgeStarts(edgeStarts), ^uint32(0))
}

type storedEdgeStarts []uint32

func (s storedEdgeStarts) Len() int {
	return len(s)
}

func (s storedEdgeStarts) Get(index uint32) uint32 {
	return s[index]
}

type storedEdgeStartReader interface {
	Len() int
	Get(uint32) uint32
}

type storedEdgeToken interface {
	~uint16 | ~uint32
}

func readEdgeTokensAs[T storedEdgeToken, S storedEdgeStartReader](
	r *bufio.Reader,
	edgeStarts S,
	maxToken uint32,
) ([]T, error) {
	edgeCount := int(edgeStarts.Get(uint32(edgeStarts.Len() - 1)))
	width, err := readUint32(r, "edge token width")
	if err != nil {
		return nil, err
	}
	if (width != shortTokenBit && width != fullTokenBit) || edgeCount == 0 {
		if edgeCount != 0 || width != 0 {
			return nil, fmt.Errorf("corpus: invalid edge token width %d", width)
		}
	}
	byteWidth := int(width) / bitsPerByte
	encoded := make([]byte, edgeCount*byteWidth)
	if _, err := io.ReadFull(r, encoded); err != nil {
		return nil, fmt.Errorf("corpus: read automaton edge tokens: %w", err)
	}

	edgeTokens := make([]T, edgeCount)
	position := 0
	for node := 0; node+1 < edgeStarts.Len(); node++ {
		var previous uint32
		for edge := edgeStarts.Get(uint32(node)); edge < edgeStarts.Get(uint32(node+1)); edge++ {
			var delta uint32
			switch width {
			case shortTokenBit:
				delta = uint32(binary.LittleEndian.Uint16(encoded[position:]))
			case fullTokenBit:
				delta = binary.LittleEndian.Uint32(encoded[position:])
			}
			position += byteWidth
			if delta == 0 || uint64(previous)+uint64(delta) > uint64(maxToken) {
				return nil, fmt.Errorf("corpus: invalid edge token delta at edge %d", edge)
			}
			previous += delta
			edgeTokens[edge] = T(previous)
		}
	}
	return edgeTokens, nil
}

func readEdgeCounts(r *bufio.Reader, nodeCount int) ([]uint32, error) {
	bitCount := trieBitFactor*nodeCount - 1
	encoded := make([]byte, (bitCount+bitsPerByte-1)/bitsPerByte)
	if _, err := io.ReadFull(r, encoded); err != nil {
		return nil, fmt.Errorf("corpus: read automaton edge counts: %w", err)
	}
	if padding := uint(bitCount % bitsPerByte); padding != 0 && encoded[len(encoded)-1]>>padding != 0 {
		return nil, errors.New("corpus: non-zero automaton edge padding")
	}

	edgeStarts := make([]uint32, nodeCount+1)
	node := 0
	var edgeCount uint32
	for position := range bitCount {
		if encoded[position/bitsPerByte]&(1<<(position%bitsPerByte)) != 0 {
			edgeCount++
			if edgeCount >= uint32(nodeCount) {
				return nil, errors.New("corpus: too many automaton edges")
			}
			continue
		}
		if node >= nodeCount {
			return nil, errors.New("corpus: too many automaton nodes")
		}
		edgeStarts[node+1] = edgeCount
		node++
	}
	if node != nodeCount || edgeCount != uint32(nodeCount-1) {
		return nil, errors.New("corpus: inconsistent automaton edge counts")
	}
	return edgeStarts, nil
}

func readCompactEdgeStarts(r *bufio.Reader, nodeCount int) (aho.CompactEdgeStarts, error) {
	bitCount := trieBitFactor*nodeCount - 1
	encoded := make([]byte, (bitCount+bitsPerByte-1)/bitsPerByte)
	if _, err := io.ReadFull(r, encoded); err != nil {
		return aho.CompactEdgeStarts{}, fmt.Errorf("corpus: read automaton edge counts: %w", err)
	}
	if padding := uint(bitCount % bitsPerByte); padding != 0 && encoded[len(encoded)-1]>>padding != 0 {
		return aho.CompactEdgeStarts{}, errors.New("corpus: non-zero automaton edge padding")
	}

	nodes := make([]uint32, 0)
	corrections := make([]int32, 0)
	node := 0
	var edgeCount, previousEdgeCount uint32
	for position := range bitCount {
		if encoded[position/bitsPerByte]&(1<<(position%bitsPerByte)) != 0 {
			edgeCount++
			if edgeCount >= uint32(nodeCount) {
				return aho.CompactEdgeStarts{}, errors.New("corpus: too many automaton edges")
			}
			continue
		}
		if node >= nodeCount {
			return aho.CompactEdgeStarts{}, errors.New("corpus: too many automaton nodes")
		}
		if edgeCount-previousEdgeCount != 1 {
			nodes = append(nodes, uint32(node))
			corrections = append(corrections, int32(edgeCount)-int32(node+1))
		}
		previousEdgeCount = edgeCount
		node++
	}
	if node != nodeCount || edgeCount != uint32(nodeCount-1) {
		return aho.CompactEdgeStarts{}, errors.New("corpus: inconsistent automaton edge counts")
	}
	table, err := aho.NewCompactEdgeStartsFromCorrections(nodeCount, nodes, corrections)
	if err != nil {
		return aho.CompactEdgeStarts{}, fmt.Errorf("corpus: compact automaton edge counts: %w", err)
	}
	return table, nil
}

func readTerminalHeads(r *bufio.Reader, nodeCount int) (aho.SparseOutputs, error) {
	count, err := readCount(r, uint64(nodeCount), "terminal head")
	if err != nil {
		return aho.SparseOutputs{}, err
	}
	nodes := make([]uint32, 0, count)
	heads := make([]uint32, 0, count)
	var previous uint64
	for range count {
		delta, err := binary.ReadUvarint(r)
		if err != nil {
			return aho.SparseOutputs{}, fmt.Errorf("corpus: read terminal node delta: %w", err)
		}
		if delta == 0 || delta > uint64(nodeCount)-previous {
			return aho.SparseOutputs{}, errors.New("corpus: invalid terminal node delta")
		}
		current := previous + delta
		head, err := readUint32(r, "terminal head")
		if err != nil {
			return aho.SparseOutputs{}, err
		}
		nodes = append(nodes, uint32(current-1))
		heads = append(heads, head)
		previous = current
	}
	return aho.NewSparseTerminalHeads(nodeCount, nodes, heads), nil
}

func readOptionalUint32s(r *bufio.Reader, count int, label string) ([]uint32, error) {
	values := make([]uint32, count)
	for index := range values {
		value, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, fmt.Errorf("corpus: read %s: %w", label, err)
		}
		if value == 0 {
			values[index] = aho.None
			continue
		}
		value--
		if value > uint64(^uint32(0)) {
			return nil, fmt.Errorf("corpus: %s value %d exceeds uint32", label, value)
		}
		values[index] = uint32(value)
	}
	return values, nil
}

func readUint32(r *bufio.Reader, label string) (uint32, error) {
	value, err := binary.ReadUvarint(r)
	if err != nil {
		return 0, fmt.Errorf("corpus: read %s: %w", label, err)
	}
	if value > uint64(^uint32(0)) {
		return 0, fmt.Errorf("corpus: %s value %d exceeds uint32", label, value)
	}
	return uint32(value), nil
}

func readString(r *bufio.Reader) (string, error) {
	value, err := readBytes(r, maxStringLen)
	return string(value), err
}

func readBytes(r *bufio.Reader, limit uint64) ([]byte, error) {
	length, err := binary.ReadUvarint(r)
	if err != nil {
		return nil, fmt.Errorf("read length: %w", err)
	}
	if length > limit {
		return nil, fmt.Errorf("value length %d exceeds limit %d", length, limit)
	}
	value := make([]byte, length)
	if _, err := io.ReadFull(r, value); err != nil {
		return nil, fmt.Errorf("read value: %w", err)
	}
	return value, nil
}

func readCount(r *bufio.Reader, limit uint64, label string) (int, error) {
	count, err := binary.ReadUvarint(r)
	if err != nil {
		return 0, fmt.Errorf("corpus: read %s count: %w", label, err)
	}
	if count > limit {
		return 0, fmt.Errorf("corpus: %s count %d exceeds limit %d", label, count, limit)
	}
	return int(count), nil
}
