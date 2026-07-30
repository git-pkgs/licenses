// Command licenses scans files and repositories for exact ScanCode license
// rule matches.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	licenses "github.com/git-pkgs/licenses"
)

const (
	exitSuccess      = 0
	exitFatal        = 1
	exitScanErrors   = 2
	exitNoDetections = 3
)

// version is set at build time via -ldflags.
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	exitCode, err := run(ctx, os.Args[1:], os.Stdout, os.Stderr, isTerminal(os.Stdout))
	stop()
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		_, _ = fmt.Fprintln(os.Stderr, "licenses:", err)
		exitCode = exitFatal
	}
	if exitCode != exitSuccess {
		os.Exit(exitCode)
	}
}

func run(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	terminal bool,
) (int, error) {
	flags := flag.NewFlagSet("licenses", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: licenses [flags] [path]")
		_, _ = fmt.Fprintln(stderr, "\nScan a file or directory (default: current directory).")
		_, _ = fmt.Fprintln(stderr, "\nFlags:")
		flags.PrintDefaults()
	}
	jsonOutput := flags.Bool("json", false, "force JSON output")
	humanOutput := flags.Bool("human", false, "force human-readable output")
	maxDepth := flags.Int("max-depth", defaultMaxDepth, "maximum directory depth (0 is unlimited)")
	maxFiles := flags.Int(
		"max-files",
		defaultMaxFiles,
		"maximum files to visit (required with all scope; 0 is unlimited)",
	)
	maxFileSize := flags.Int64(
		"max-file-size",
		defaultMaxFileSize,
		"maximum bytes per file (0 removes the memory guard)",
	)
	workers := flags.Int(
		"workers",
		defaultWorkerCount(),
		"number of concurrent file matchers (maximum 16)",
	)
	skip := flags.String("skip", "", "additional directory names to skip, comma-separated")
	scope := flags.String(
		"scope",
		scopeProject,
		"scan scope: project skips dependencies; all includes them; .git is always skipped",
	)
	matchedText := flags.Bool("matched-text", false, "include matched text in JSON output")
	showVersion := flags.Bool("version", false, "print the licenses and corpus versions")
	if err := flags.Parse(args); err != nil {
		return exitFatal, err
	}
	if *showVersion {
		return exitSuccess, writeVersion(stdout)
	}
	if *jsonOutput && *humanOutput {
		return exitFatal, errors.New("-json and -human cannot be used together")
	}
	if flags.NArg() > 1 {
		return exitFatal, errors.New("expected at most one file or directory")
	}
	if *scope != scopeProject && *scope != scopeAll {
		return exitFatal, fmt.Errorf("unknown scope %q: want project or all", *scope)
	}

	root := "."
	if flags.NArg() == 1 {
		root = flags.Arg(0)
	}
	maxFilesExplicit := false
	flags.Visit(func(option *flag.Flag) {
		if option.Name == "max-files" {
			maxFilesExplicit = true
		}
	})
	if *scope == scopeAll && !maxFilesExplicit {
		return exitFatal, errors.New(
			"-scope all requires -max-files; use -max-files 0 for unlimited",
		)
	}
	options := scanOptions{
		MaxDepth:      *maxDepth,
		MaxFiles:      *maxFiles,
		MaxFileSize:   *maxFileSize,
		Workers:       *workers,
		SkipDirs:      parseSkippedDirectories(*skip),
		NoDefaultSkip: *scope == scopeAll,
	}
	if err := validateScanOptions(options); err != nil {
		return exitFatal, err
	}

	var matcherOptions []licenses.Option
	if *matchedText {
		matcherOptions = append(matcherOptions, licenses.WithMatchedText())
	}
	matcher, err := licenses.New(matcherOptions...)
	if err != nil {
		return exitFatal, err
	}
	report, err := scanRepository(ctx, matcher, root, options)
	if err != nil {
		return exitFatal, err
	}
	if *jsonOutput || (!*humanOutput && !terminal) {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return exitFatal, err
		}
	} else if err := writeHuman(stdout, report); err != nil {
		return exitFatal, err
	}
	return reportExitCode(report), nil
}

func writeVersion(writer io.Writer) error {
	matcher, err := licenses.New()
	if err != nil {
		return err
	}
	corpus := matcher.Corpus()
	_, err = fmt.Fprintf(
		writer,
		"licenses %s (ScanCode %s, %d rules, commit %s)\n",
		version,
		corpus.Version,
		corpus.RuleCount,
		corpus.SourceCommit,
	)
	return err
}

func reportExitCode(report scanReport) int {
	if len(report.Errors) != 0 || report.Summary.Truncated {
		return exitScanErrors
	}
	if len(report.Expressions) == 0 {
		return exitNoDetections
	}
	return exitSuccess
}

func parseSkippedDirectories(value string) map[string]bool {
	directories := make(map[string]bool)
	for name := range strings.SplitSeq(value, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			directories[name] = true
		}
	}
	return directories
}

func writeHuman(writer io.Writer, report scanReport) error {
	if _, err := fmt.Fprintf(
		writer,
		"%s\n%s scope; ScanCode %s, %d rules, commit %s\n",
		report.Root,
		report.Scope,
		report.Corpus.Version,
		report.Corpus.RuleCount,
		report.Corpus.SourceCommit,
	); err != nil {
		return err
	}
	if err := writeHumanExpressions(writer, report.Expressions); err != nil {
		return err
	}
	if err := writeHumanFiles(writer, report.Files); err != nil {
		return err
	}
	if err := writeHumanSkipped(writer, report.Skipped); err != nil {
		return err
	}
	if err := writeHumanErrors(writer, report.Errors); err != nil {
		return err
	}
	return writeHumanSummary(writer, report.Summary)
}

func writeHumanSkipped(writer io.Writer, skipped []skipRecord) error {
	if len(skipped) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(writer, "\nSkipped:"); err != nil {
		return err
	}
	for _, record := range skipped {
		if _, err := fmt.Fprintf(writer, "  %s: %s\n", record.Path, record.Reason); err != nil {
			return err
		}
	}
	return nil
}

func writeHumanExpressions(writer io.Writer, expressions []expressionRecord) error {
	if len(expressions) == 0 {
		if _, err := fmt.Fprintln(writer, "\nNo conclusive license detections."); err != nil {
			return err
		}
		return nil
	}
	if _, err := fmt.Fprintln(writer, "\nDetected expressions:"); err != nil {
		return err
	}
	for _, expression := range expressions {
		if _, err := fmt.Fprintf(
			writer,
			"  %s: %s, %d files, %d matches\n",
			expression.Expression,
			expression.Identification,
			expression.Files,
			expression.Matches,
		); err != nil {
			return err
		}
	}
	return nil
}

func writeHumanFiles(writer io.Writer, files []fileRecord) error {
	for _, file := range files {
		if _, err := fmt.Fprintf(writer, "\n%s (%s)\n", file.Path, formatBytes(file.Size)); err != nil {
			return err
		}
		for _, detection := range file.Detections {
			if _, err := fmt.Fprintf(
				writer,
				"  %s [%s]\n",
				detection.Expression,
				detection.Identification,
			); err != nil {
				return err
			}
			for _, match := range detection.Matches {
				if err := writeHumanMatch(writer, "    ", match); err != nil {
					return err
				}
			}
		}
		for _, clue := range file.Clues {
			if err := writeHumanMatch(writer, "  clue ", clue); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeHumanErrors(writer io.Writer, scanErrors []scanErrorRecord) error {
	if len(scanErrors) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(writer, "\nErrors:"); err != nil {
		return err
	}
	for _, scanError := range scanErrors {
		if _, err := fmt.Fprintf(writer, "  %s: %s\n", scanError.Path, scanError.Error); err != nil {
			return err
		}
	}
	return nil
}

func writeHumanSummary(writer io.Writer, summary scanSummary) error {
	_, err := fmt.Fprintf(
		writer,
		"\nScanned %d/%d files (%s); %d files detected; "+
			"identified in %d, partial in %d, NOASSERTION in %d; "+
			"%d files with clues, %d errors",
		summary.FilesScanned,
		summary.FilesVisited,
		formatBytes(summary.BytesScanned),
		summary.FilesWithDetections,
		summary.FilesWithIdentifiedDetections,
		summary.FilesWithPartialDetections,
		summary.FilesWithNoAssertionDetections,
		summary.FilesWithClues,
		summary.ErrorCount,
	)
	if err != nil {
		return err
	}
	if summary.Truncated {
		_, err = fmt.Fprint(writer, "; file limit reached")
	}
	if err != nil {
		return err
	}
	skippedFiles := summary.FilesSkippedBinary +
		summary.FilesSkippedSize +
		summary.FilesSkippedOther
	if skippedFiles != 0 || summary.DirectoriesSkipped != 0 {
		_, err = fmt.Fprintf(
			writer,
			"; skipped %d files and %d directories",
			skippedFiles,
			summary.DirectoriesSkipped,
		)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(writer)
	return err
}

func writeHumanMatch(writer io.Writer, prefix string, match matchRecord) error {
	_, err := fmt.Fprintf(
		writer,
		"%s%s %s %s score %.0f coverage %.0f bytes %d:%d\n",
		prefix,
		match.RuleID,
		match.Kind,
		match.Method,
		match.Score,
		match.Coverage,
		match.Start,
		match.End,
	)
	return err
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
