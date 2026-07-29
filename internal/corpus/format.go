package corpus

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/git-pkgs/licenses/internal/aho"
)

const (
	// FormatVersion is the on-disk corpus index format.
	FormatVersion = 2

	FlagLicenseText uint16 = 1 << iota
	FlagLicenseNotice
	FlagLicenseTag
	FlagLicenseReference
	FlagLicenseIntro
	FlagLicenseClue
	FlagFalsePositive
	FlagRequiredPhrase
	FlagContinuous
	FlagDeprecated
)

const (
	maxRuleCount  = 100_000
	maxWordCount  = 1_000_000
	maxTokenCount = 1_000_000
	maxNodeCount  = 10_000_000
	maxEdgeCount  = 10_000_000
	maxStringLen  = 16 << 20
	maxListLen    = 10_000
	bufferSize    = 256 << 10
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
	ID                  string
	Expression          string
	Text                []byte
	Tokens              []uint32
	Language            string
	ReferencedFilenames []string
	RequiredPhrases     []string
	Flags               uint16
	Relevance           uint8
	MinimumCoverage     uint8
}

// Index is a decoded corpus.
type Index struct {
	Info       Info
	Vocabulary []string
	Rules      []Rule
	Automaton  aho.Automaton
}

// Write encodes index as a deterministic gzip-compressed binary stream.
func Write(w io.Writer, index Index) error {
	if err := validateIndex(index); err != nil {
		return err
	}

	zw, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("corpus: create compressor: %w", err)
	}
	zw.ModTime = time.Unix(0, 0).UTC()
	zw.OS = unknownOS
	bw := bufio.NewWriterSize(zw, bufferSize)

	if err := writeHeader(bw, index.Info); err != nil {
		_ = zw.Close()
		return err
	}
	if err := writeStrings(bw, index.Vocabulary); err != nil {
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
	if err := writeAutomaton(bw, index.Automaton); err != nil {
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

func validateIndex(index Index) error {
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
	}
	if err := index.Automaton.Validate(len(rules)); err != nil {
		return fmt.Errorf("corpus: invalid automaton: %w", err)
	}
	return nil
}

func writeHeader(w io.Writer, info Info) error {
	if _, err := w.Write(magic[:]); err != nil {
		return fmt.Errorf("corpus: write magic: %w", err)
	}
	if err := writeUvarint(w, FormatVersion); err != nil {
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
	if err := binary.Write(w, binary.LittleEndian, rule.Flags); err != nil {
		return fmt.Errorf("corpus: write flags for %q: %w", rule.ID, err)
	}
	if _, err := w.Write([]byte{rule.Relevance, rule.MinimumCoverage}); err != nil {
		return fmt.Errorf("corpus: write thresholds for %q: %w", rule.ID, err)
	}
	if err := writeString(w, rule.Language); err != nil {
		return err
	}
	if err := writeStrings(w, rule.ReferencedFilenames); err != nil {
		return err
	}
	if err := writeStrings(w, rule.RequiredPhrases); err != nil {
		return err
	}
	if err := writeUint32s(w, rule.Tokens); err != nil {
		return err
	}
	return nil
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

func writeAutomaton(w io.Writer, automaton aho.Automaton) error {
	nodeCount := automaton.NodeCount()
	if err := writeUvarint(w, uint64(nodeCount)); err != nil {
		return err
	}
	for node := range nodeCount {
		edgeCount := automaton.EdgeStarts[node+1] - automaton.EdgeStarts[node]
		if err := writeUvarint(w, uint64(edgeCount)); err != nil {
			return err
		}
	}
	for _, head := range automaton.TerminalHeads {
		if err := writeOptional(w, head); err != nil {
			return err
		}
	}
	for node := range nodeCount {
		start, end := automaton.EdgeStarts[node], automaton.EdgeStarts[node+1]
		var previous uint32
		for edge := start; edge < end; edge++ {
			token := automaton.EdgeTokens[edge]
			if err := writeUvarint(w, uint64(token-previous)); err != nil {
				return err
			}
			previous = token
		}
	}
	for _, next := range automaton.OutputNext {
		if err := writeOptional(w, next); err != nil {
			return err
		}
	}
	return nil
}

func writeOptional(w io.Writer, value uint32) error {
	if value == aho.None {
		return writeUvarint(w, 0)
	}
	return writeUvarint(w, uint64(value)+1)
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
	zr, err := gzip.NewReader(r)
	if err != nil {
		return Index{}, fmt.Errorf("corpus: open compressed index: %w", err)
	}
	defer func() { _ = zr.Close() }()

	br := bufio.NewReaderSize(zr, bufferSize)
	info, err := readHeader(br)
	if err != nil {
		return Index{}, err
	}
	vocabulary, err := readStrings(br, maxWordCount)
	if err != nil {
		return Index{}, fmt.Errorf("corpus: read vocabulary: %w", err)
	}
	for index, word := range vocabulary {
		if word == "" {
			return Index{}, fmt.Errorf("corpus: vocabulary word %d is empty", index)
		}
		if index > 0 && vocabulary[index-1] >= word {
			return Index{}, fmt.Errorf("corpus: vocabulary is not strictly sorted at %q", word)
		}
	}
	count, err := readCount(br, maxRuleCount, "rule")
	if err != nil {
		return Index{}, err
	}
	rules := make([]Rule, count)
	for i := range rules {
		rules[i], err = readRule(br)
		if err != nil {
			return Index{}, fmt.Errorf("corpus: read rule %d: %w", i, err)
		}
		if i > 0 && rules[i-1].ID >= rules[i].ID {
			return Index{}, fmt.Errorf("corpus: rules are not strictly sorted at %q", rules[i].ID)
		}
		for _, token := range rules[i].Tokens {
			if token == 0 || uint64(token) > uint64(len(vocabulary)) {
				return Index{}, fmt.Errorf("corpus: rule %q has invalid token %d", rules[i].ID, token)
			}
		}
	}
	automaton, err := readAutomaton(br, count)
	if err != nil {
		return Index{}, err
	}
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		if err == nil {
			return Index{}, errors.New("corpus: trailing data")
		}
		return Index{}, fmt.Errorf("corpus: finish index: %w", err)
	}
	info.RuleCount = count
	return Index{
		Info:       info,
		Vocabulary: vocabulary,
		Rules:      rules,
		Automaton:  automaton,
	}, nil
}

func readHeader(r *bufio.Reader) (Info, error) {
	var gotMagic [len(magic)]byte
	if _, err := io.ReadFull(r, gotMagic[:]); err != nil {
		return Info{}, fmt.Errorf("corpus: read magic: %w", err)
	}
	if gotMagic != magic {
		return Info{}, errors.New("corpus: invalid magic")
	}
	version, err := binary.ReadUvarint(r)
	if err != nil {
		return Info{}, fmt.Errorf("corpus: read format version: %w", err)
	}
	if version != FormatVersion {
		return Info{}, fmt.Errorf("corpus: unsupported format version %d", version)
	}
	corpusVersion, err := readString(r)
	if err != nil {
		return Info{}, err
	}
	sourceCommit, err := readString(r)
	if err != nil {
		return Info{}, err
	}
	return Info{Version: corpusVersion, SourceCommit: sourceCommit}, nil
}

func readRule(r *bufio.Reader) (Rule, error) {
	id, err := readString(r)
	if err != nil {
		return Rule{}, err
	}
	expression, err := readString(r)
	if err != nil {
		return Rule{}, err
	}
	var flags uint16
	if err := binary.Read(r, binary.LittleEndian, &flags); err != nil {
		return Rule{}, fmt.Errorf("read flags: %w", err)
	}
	var thresholds [2]byte
	if _, err := io.ReadFull(r, thresholds[:]); err != nil {
		return Rule{}, fmt.Errorf("read thresholds: %w", err)
	}
	language, err := readString(r)
	if err != nil {
		return Rule{}, err
	}
	referencedFilenames, err := readStrings(r, maxListLen)
	if err != nil {
		return Rule{}, err
	}
	requiredPhrases, err := readStrings(r, maxListLen)
	if err != nil {
		return Rule{}, err
	}
	tokens, err := readUint32s(r, maxTokenCount)
	if err != nil {
		return Rule{}, err
	}
	return Rule{
		ID:                  id,
		Expression:          expression,
		Tokens:              tokens,
		Language:            language,
		ReferencedFilenames: referencedFilenames,
		RequiredPhrases:     requiredPhrases,
		Flags:               flags,
		Relevance:           thresholds[0],
		MinimumCoverage:     thresholds[1],
	}, nil
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

func readAutomaton(r *bufio.Reader, valueCount int) (aho.Automaton, error) {
	nodeCount, err := readCount(r, maxNodeCount, "automaton node")
	if err != nil {
		return aho.Automaton{}, err
	}
	if nodeCount == 0 {
		return aho.Automaton{}, errors.New("corpus: automaton has no root")
	}
	edgeStarts := make([]uint32, nodeCount+1)
	var edgeCount uint64
	for node := range nodeCount {
		count, err := binary.ReadUvarint(r)
		if err != nil {
			return aho.Automaton{}, fmt.Errorf("corpus: read edge count for node %d: %w", node, err)
		}
		edgeCount += count
		if edgeCount > maxEdgeCount {
			return aho.Automaton{}, fmt.Errorf("corpus: edge count %d exceeds limit %d", edgeCount, maxEdgeCount)
		}
		edgeStarts[node+1] = uint32(edgeCount)
	}

	terminalHeads, err := readOptionalUint32s(r, nodeCount, "terminal head")
	if err != nil {
		return aho.Automaton{}, err
	}

	edgeTokens := make([]uint32, edgeCount)
	for node := range nodeCount {
		start, end := edgeStarts[node], edgeStarts[node+1]
		var previous uint32
		for edge := start; edge < end; edge++ {
			delta, err := readUint32(r, "edge token delta")
			if err != nil {
				return aho.Automaton{}, err
			}
			if delta == 0 || uint64(previous)+uint64(delta) > uint64(^uint32(0)) {
				return aho.Automaton{}, fmt.Errorf("corpus: invalid edge token delta at edge %d", edge)
			}
			edgeTokens[edge] = previous + delta
			previous = edgeTokens[edge]
		}
	}
	outputNext, err := readOptionalUint32s(r, valueCount, "output chain")
	if err != nil {
		return aho.Automaton{}, err
	}
	failures, outputLinks, err := aho.BuildFailureLinks(edgeStarts, edgeTokens, terminalHeads)
	if err != nil {
		return aho.Automaton{}, fmt.Errorf("corpus: build automaton links: %w", err)
	}

	automaton := aho.Automaton{
		EdgeStarts:    edgeStarts,
		EdgeTokens:    edgeTokens,
		Failures:      failures,
		OutputLinks:   outputLinks,
		TerminalHeads: terminalHeads,
		OutputNext:    outputNext,
	}
	if err := automaton.Validate(valueCount); err != nil {
		return aho.Automaton{}, fmt.Errorf("corpus: invalid automaton: %w", err)
	}
	return automaton, nil
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
