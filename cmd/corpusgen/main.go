// Command corpusgen builds the embedded license corpus from a ScanCode checkout.
package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/git-pkgs/licenses/internal/aho"
	"github.com/git-pkgs/licenses/internal/corpus"
	"github.com/git-pkgs/licenses/internal/tokenize"
	"gopkg.in/yaml.v3"
)

const (
	fileMode             = 0o644
	directoryMode        = 0o755
	sha1ObjectIDLength   = 40
	sha256ObjectIDLength = 64
)

type sourceVersion struct {
	Version string
	Commit  string
}

type metadata struct {
	Key                  string   `yaml:"key"`
	SPDXLicenseKey       string   `yaml:"spdx_license_key"`
	OtherSPDXLicenseKeys []string `yaml:"other_spdx_license_keys"`
	LicenseExpression    string   `yaml:"license_expression"`
	Relevance            *int     `yaml:"relevance"`
	IsLicenseText        bool     `yaml:"is_license_text"`
	IsLicenseNotice      bool     `yaml:"is_license_notice"`
	IsLicenseTag         bool     `yaml:"is_license_tag"`
	IsLicenseReference   bool     `yaml:"is_license_reference"`
	IsLicenseIntro       bool     `yaml:"is_license_intro"`
	IsLicenseClue        bool     `yaml:"is_license_clue"`
	IsFalsePositive      bool     `yaml:"is_false_positive"`
	IsRequiredPhrase     bool     `yaml:"is_required_phrase"`
	IsContinuous         bool     `yaml:"is_continuous"`
	IsDeprecated         bool     `yaml:"is_deprecated"`
}

func main() {
	scancode := flag.String("scancode", "", "path to a checked-out scancode-toolkit tree")
	output := flag.String("output", "internal/corpus/corpus.bin.gz", "output index path")
	versionFile := flag.String("version-file", "CORPUS_VERSION", "pinned corpus version file")
	flag.Parse()

	started := time.Now()
	if err := run(*scancode, *versionFile, *output); err != nil {
		fmt.Fprintln(os.Stderr, "corpusgen:", err)
		os.Exit(1)
	}
	info, err := os.Stat(*output)
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpusgen:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d bytes) in %s\n", *output, info.Size(), time.Since(started).Round(time.Millisecond))
}

func run(scancode, versionFile, output string) error {
	if scancode == "" {
		return errors.New("-scancode is required")
	}
	version, err := readSourceVersion(versionFile)
	if err != nil {
		return err
	}
	if err := verifyCheckout(scancode, version.Commit); err != nil {
		return err
	}
	index, err := buildIndex(scancode, version)
	if err != nil {
		return err
	}
	return writeIndex(output, index)
}

func readSourceVersion(path string) (sourceVersion, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return sourceVersion{}, fmt.Errorf("read corpus version: %w", err)
	}
	var version sourceVersion
	for lineNumber, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(value) == "" {
			return sourceVersion{}, fmt.Errorf("%s:%d: expected key=value", path, lineNumber+1)
		}
		switch strings.TrimSpace(key) {
		case "version":
			if version.Version != "" {
				return sourceVersion{}, fmt.Errorf("%s:%d: duplicate version", path, lineNumber+1)
			}
			version.Version = strings.TrimSpace(value)
		case "commit":
			if version.Commit != "" {
				return sourceVersion{}, fmt.Errorf("%s:%d: duplicate commit", path, lineNumber+1)
			}
			version.Commit = strings.ToLower(strings.TrimSpace(value))
		default:
			return sourceVersion{}, fmt.Errorf("%s:%d: unknown key %q", path, lineNumber+1, key)
		}
	}
	if version.Version == "" {
		return sourceVersion{}, fmt.Errorf("%s: missing version", path)
	}
	switch len(version.Commit) {
	case sha1ObjectIDLength, sha256ObjectIDLength:
	default:
		return sourceVersion{}, fmt.Errorf("%s: commit must be a full 40- or 64-character object ID", path)
	}
	if _, err := hex.DecodeString(version.Commit); err != nil {
		return sourceVersion{}, fmt.Errorf("%s: invalid commit: %w", path, err)
	}
	return version, nil
}

func verifyCheckout(root, wantCommit string) error {
	command := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("read ScanCode checkout commit: %w", err)
	}
	gotCommit := strings.TrimSpace(string(output))
	if gotCommit != wantCommit {
		return fmt.Errorf("ScanCode checkout is at %s, CORPUS_VERSION pins %s", gotCommit, wantCommit)
	}

	dataPath := filepath.Join("src", "licensedcode", "data")
	command = exec.Command("git", "-C", root, "status", "--porcelain", "--untracked-files=all", "--", dataPath)
	output, err = command.Output()
	if err != nil {
		return fmt.Errorf("check ScanCode corpus status: %w", err)
	}
	if len(bytes.TrimSpace(output)) != 0 {
		return errors.New("ScanCode corpus has uncommitted changes")
	}
	return nil
}

func buildIndex(root string, version sourceVersion) (corpus.Index, error) {
	dataRoot := filepath.Join(root, "src", "licensedcode", "data")
	licensesDirectory := filepath.Join(dataRoot, "licenses")
	licenses, err := loadDirectory(licensesDirectory, ".LICENSE", true)
	if err != nil {
		return corpus.Index{}, err
	}
	rules, err := loadDirectory(filepath.Join(dataRoot, "rules"), ".RULE", false)
	if err != nil {
		return corpus.Index{}, err
	}
	spdxKeys, reportingIDs, err := loadSPDXMappings(licensesDirectory)
	if err != nil {
		return corpus.Index{}, err
	}
	records := make([]corpus.Rule, 0, len(licenses)+len(rules))
	records = append(records, licenses...)
	records = append(records, rules...)
	slices.SortFunc(records, func(first, second corpus.Rule) int {
		return strings.Compare(first.ID, second.ID)
	})

	texts := make([][]byte, len(records))
	for index := range records {
		texts[index] = records[index].Text
	}
	vocabulary, err := tokenize.NewVocabulary(texts)
	if err != nil {
		return corpus.Index{}, fmt.Errorf("build vocabulary: %w", err)
	}
	patterns := make([]aho.Pattern, 0, len(records))
	for index := range records {
		tokens := vocabulary.Tokenize(records[index].Text)
		records[index].Tokens = make([]uint32, len(tokens.IDs))
		for position, id := range tokens.IDs {
			records[index].Tokens[position] = uint32(id)
		}
		records[index].Text = nil
		if len(records[index].Tokens) != 0 {
			patterns = append(patterns, aho.Pattern{
				Tokens: records[index].Tokens,
				Value:  uint32(index),
			})
		}
	}
	automaton, err := aho.Build(patterns, len(records))
	if err != nil {
		return corpus.Index{}, fmt.Errorf("build exact automaton: %w", err)
	}
	return corpus.Index{
		Info: corpus.Info{
			Version:      version.Version,
			RuleCount:    len(records),
			SourceCommit: version.Commit,
		},
		Vocabulary:   vocabulary.Words(),
		Rules:        records,
		Automaton:    automaton,
		SPDXKeys:     spdxKeys,
		ReportingIDs: reportingIDs,
	}, nil
}

// loadSPDXMappings builds maps between ScanCode keys and SPDX identifiers from
// the licenses directory. Input precedence on conflict is: a license's own
// key, then its primary spdx_license_key, then other_spdx_license_keys aliases.
// ScanCode's data contains a small number of aliases that collide with another
// license's key.
func loadSPDXMappings(path string) (map[string]string, map[string]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	metas := make([]metadata, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".LICENSE" {
			continue
		}
		licensePath := filepath.Join(path, entry.Name())
		data, err := os.ReadFile(licensePath)
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", licensePath, err)
		}
		frontmatter, _, err := splitFrontmatter(data)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", licensePath, err)
		}
		var meta metadata
		if err := yaml.Unmarshal(frontmatter, &meta); err != nil {
			return nil, nil, fmt.Errorf("%s: parse metadata: %w", licensePath, err)
		}
		if meta.Key == "" {
			return nil, nil, fmt.Errorf("%s: missing key", licensePath)
		}
		metas = append(metas, meta)
	}

	keys := make(map[string]string, len(metas))
	reportingIDs := make(map[string]string, len(metas))
	for _, meta := range metas {
		reportingID := meta.SPDXLicenseKey
		if reportingID == "" {
			reportingID = "LicenseRef-scancode-" + meta.Key
		}
		reportingIDs[strings.ToLower(meta.Key)] = reportingID
	}
	for _, meta := range metas {
		addSPDXKey(keys, meta.Key, meta.Key)
	}
	for _, meta := range metas {
		addSPDXKey(keys, reportingIDs[strings.ToLower(meta.Key)], meta.Key)
	}
	for _, meta := range metas {
		for _, other := range meta.OtherSPDXLicenseKeys {
			addSPDXKey(keys, other, meta.Key)
		}
	}
	return keys, reportingIDs, nil
}

func addSPDXKey(keys map[string]string, spdx, scancode string) {
	if spdx == "" {
		return
	}
	lower := strings.ToLower(spdx)
	if _, exists := keys[lower]; exists {
		return
	}
	keys[lower] = scancode
}

func loadDirectory(path, extension string, licenseText bool) ([]corpus.Rule, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	rules := make([]corpus.Rule, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != extension {
			continue
		}
		rule, err := loadRule(filepath.Join(path, entry.Name()), licenseText)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func loadRule(path string, licenseText bool) (corpus.Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return corpus.Rule{}, fmt.Errorf("read %s: %w", path, err)
	}
	frontmatter, text, err := splitFrontmatter(data)
	if err != nil {
		return corpus.Rule{}, fmt.Errorf("%s: %w", path, err)
	}
	var meta metadata
	if err := yaml.Unmarshal(frontmatter, &meta); err != nil {
		return corpus.Rule{}, fmt.Errorf("%s: parse metadata: %w", path, err)
	}

	expression := meta.LicenseExpression
	if licenseText {
		expression = meta.Key
		meta.IsLicenseText = true
	}
	if expression == "" && !meta.IsFalsePositive {
		return corpus.Rule{}, fmt.Errorf("%s: missing license expression", path)
	}
	relevance := 100
	if meta.Relevance != nil {
		relevance = *meta.Relevance
	}
	if relevance < 0 || relevance > 100 {
		return corpus.Rule{}, fmt.Errorf("%s: relevance %d is outside 0-100", path, relevance)
	}
	return corpus.Rule{
		ID:         filepath.Base(path),
		Expression: expression,
		Text:       text,
		Flags:      flags(meta),
		Relevance:  uint8(relevance),
	}, nil
}

func flags(meta metadata) uint16 {
	var value uint16
	if meta.IsLicenseText {
		value |= corpus.FlagLicenseText
	}
	if meta.IsLicenseNotice {
		value |= corpus.FlagLicenseNotice
	}
	if meta.IsLicenseTag {
		value |= corpus.FlagLicenseTag
	}
	if meta.IsLicenseReference {
		value |= corpus.FlagLicenseReference
	}
	if meta.IsLicenseIntro {
		value |= corpus.FlagLicenseIntro
	}
	if meta.IsLicenseClue {
		value |= corpus.FlagLicenseClue
	}
	if meta.IsFalsePositive {
		value |= corpus.FlagFalsePositive
	}
	if meta.IsRequiredPhrase {
		value |= corpus.FlagRequiredPhrase
	}
	if meta.IsContinuous {
		value |= corpus.FlagContinuous
	}
	if meta.IsDeprecated {
		value |= corpus.FlagDeprecated
	}
	return value
}

func splitFrontmatter(data []byte) ([]byte, []byte, error) {
	lineStart := 0
	metadataStart := -1
	for lineStart < len(data) {
		lineEnd := bytes.IndexByte(data[lineStart:], '\n')
		nextLine := len(data)
		if lineEnd >= 0 {
			lineEnd += lineStart
			nextLine = lineEnd + 1
		} else {
			lineEnd = len(data)
		}
		line := bytes.TrimSpace(data[lineStart:lineEnd])
		line = bytes.TrimPrefix(line, []byte{0xef, 0xbb, 0xbf})
		if len(line) != 0 {
			if string(line) != "---" {
				return nil, nil, errors.New("missing opening YAML delimiter")
			}
			metadataStart = nextLine
			break
		}
		lineStart = nextLine
	}
	if metadataStart == -1 {
		return nil, nil, errors.New("missing opening YAML delimiter")
	}
	lineStart = metadataStart
	for lineStart <= len(data) {
		lineEnd := bytes.IndexByte(data[lineStart:], '\n')
		nextLine := len(data)
		if lineEnd >= 0 {
			lineEnd += lineStart
			nextLine = lineEnd + 1
		} else {
			lineEnd = len(data)
		}
		if string(bytes.TrimSpace(data[lineStart:lineEnd])) == "---" {
			return data[metadataStart:lineStart], data[nextLine:], nil
		}
		if nextLine == len(data) {
			break
		}
		lineStart = nextLine
	}
	return nil, nil, errors.New("missing closing YAML delimiter")
}

func writeIndex(path string, index corpus.Index) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, directoryMode); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	temporary, err := os.CreateTemp(parent, ".corpus-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary index: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()

	if err := temporary.Chmod(fileMode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set index permissions: %w", err)
	}
	if err := corpus.Write(temporary, index); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close index: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace index: %w", err)
	}
	return nil
}
