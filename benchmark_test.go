package licenses

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/git-pkgs/licenses/internal/corpus"
	"github.com/git-pkgs/licenses/internal/tokenize"
)

var (
	benchmarkMatcher        *Matcher
	benchmarkResult         Result
	benchmarkSPDXExpression string
	benchmarkSPDXIDs        []string
	benchmarkExactMatches   []exactMatch
	benchmarkTokenIDs       tokenize.IDTokens
	benchmarkTokenOffsets   []tokenize.Offset
)

func BenchmarkMatcherColdStart(b *testing.B) {
	b.ReportAllocs()
	// Keep every matcher live through the final GC so retained-B/op measures
	// the runtime engine rather than transient corpus-decoding memory.
	matchers := make([]*Matcher, 0, b.N)
	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	b.ResetTimer()
	for b.Loop() {
		index, err := corpus.Load()
		if err != nil {
			b.Fatal(err)
		}
		matcher, err := newMatcher(index)
		if err != nil {
			b.Fatal(err)
		}
		matchers = append(matchers, matcher)
	}
	b.StopTimer()
	var after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&after)
	if after.HeapAlloc >= before.HeapAlloc {
		b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/float64(b.N), "retained-B/op")
	}
	runtime.KeepAlive(matchers)
}

func BenchmarkMatcherNewWarm(b *testing.B) {
	if _, err := New(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		var err error
		benchmarkMatcher, err = New()
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMatchApacheHash(b *testing.B) {
	matcher, input := benchmarkRule(b, "apache-2.0.LICENSE")
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		var err error
		benchmarkResult, err = matcher.Match(context.Background(), input)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMatchApacheExact(b *testing.B) {
	matcher, rule := benchmarkRule(b, "apache-2.0.LICENSE")
	input := make([]byte, 0, len(rule)+32)
	input = append(input, "unmatched-prefix "...)
	input = append(input, rule...)
	input = append(input, " unmatched-suffix"...)
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		var err error
		benchmarkResult, err = matcher.Match(context.Background(), input)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMatchCorpusHash(b *testing.B) {
	matcher, err := New()
	if err != nil {
		b.Fatal(err)
	}
	index, err := corpus.Load()
	if err != nil {
		b.Fatal(err)
	}
	vocabulary, err := tokenize.NewVocabularyFromWords(index.Vocabulary)
	if err != nil {
		b.Fatal(err)
	}
	inputs := make([][]byte, 0, len(index.Rules))
	var byteCount int64
	for _, rule := range index.Rules {
		if len(rule.Tokens) == 0 {
			continue
		}
		input := normalizedRuleText(vocabulary, rule)
		inputs = append(inputs, input)
		byteCount += int64(len(input))
	}
	index = corpus.Index{}
	runtime.GC()
	b.SetBytes(byteCount)
	b.ResetTimer()
	for b.Loop() {
		for _, input := range inputs {
			result, err := matcher.Match(context.Background(), input)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkResult = result
		}
	}
}

func BenchmarkMatchSPDXTag(b *testing.B) {
	matcher, err := New()
	if err != nil {
		b.Fatal(err)
	}
	input := []byte(
		"// SPDX-License-Identifier: MIT OR ISC AND LicenseRef-scancode-bsd-new\n",
	)
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkResult, err = matcher.Match(context.Background(), input)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNormalizeSPDXExpression(b *testing.B) {
	matcher, err := New()
	if err != nil {
		b.Fatal(err)
	}
	input := []byte("MIT OR ISC AND LicenseRef-scancode-bsd-new")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		benchmarkSPDXExpression, benchmarkSPDXIDs, _ = matcher.engine.spdx.normalizeExpression(input)
	}
}

func BenchmarkMatchRepeatedShortNotice(b *testing.B) {
	matcher, err := New()
	if err != nil {
		b.Fatal(err)
	}
	for _, repetitions := range []int{5_000, 10_000, 15_000} {
		input := []byte(strings.Repeat("mit license ", repetitions))
		tokens := matcher.engine.vocabulary.TokenizeIDs(input)
		candidates, method, err := matcher.engine.collectExactMatches(
			context.Background(),
			tokens.IDs,
		)
		if err != nil {
			b.Fatal(err)
		}
		if method != Exact {
			b.Fatalf("candidate method = %q, want %q", method, Exact)
		}
		b.Run(fmt.Sprint(repetitions), func(b *testing.B) {
			b.SetBytes(int64(len(input)))
			for b.Loop() {
				result, err := matcher.Match(context.Background(), input)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkResult = result
			}
			b.ReportMetric(float64(len(candidates)), "candidates/op")
		})
	}
}

func BenchmarkMatchUnmatchedRepetitiveInput(b *testing.B) {
	matcher, err := New()
	if err != nil {
		b.Fatal(err)
	}
	for _, repetitions := range []int{5_000, 10_000, 20_000} {
		input := []byte(strings.Repeat("miss ", repetitions))
		b.Run(fmt.Sprint(repetitions), func(b *testing.B) {
			b.SetBytes(int64(len(input)))
			for b.Loop() {
				result, err := matcher.Match(context.Background(), input)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkResult = result
			}
		})
	}
}

func BenchmarkCollectExactMatchCandidates(b *testing.B) {
	matcher, err := New()
	if err != nil {
		b.Fatal(err)
	}
	for _, repetitions := range []int{5_000, 10_000, 15_000} {
		input := []byte(strings.Repeat("mit license ", repetitions))
		tokens := matcher.engine.vocabulary.TokenizeIDs(input)
		candidates, method, err := matcher.engine.collectExactMatches(
			context.Background(),
			tokens.IDs,
		)
		if err != nil {
			b.Fatal(err)
		}
		if method != Exact {
			b.Fatalf("candidate method = %q, want %q", method, Exact)
		}
		b.Run(fmt.Sprint(len(candidates)), func(b *testing.B) {
			b.SetBytes(int64(len(input)))
			for b.Loop() {
				var collectErr error
				benchmarkExactMatches, _, collectErr = matcher.engine.collectExactMatches(
					context.Background(),
					tokens.IDs,
				)
				if collectErr != nil {
					b.Fatal(collectErr)
				}
			}
			b.ReportMetric(float64(len(candidates)), "candidates/op")
		})
	}
}

func BenchmarkFilterExactMatchCandidates(b *testing.B) {
	matcher, err := New()
	if err != nil {
		b.Fatal(err)
	}
	for _, repetitions := range []int{5_000, 10_000, 15_000} {
		input := []byte(strings.Repeat("mit license ", repetitions))
		tokens := matcher.engine.vocabulary.TokenizeIDs(input)
		candidates, _, err := matcher.engine.collectExactMatches(
			context.Background(),
			tokens.IDs,
		)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprint(len(candidates)), func(b *testing.B) {
			for b.Loop() {
				matches := slices.Clone(candidates)
				benchmarkExactMatches, err = filterExactMatches(
					context.Background(),
					matcher.engine,
					matches,
					allExactFilters,
				)
				if err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(len(candidates)), "candidates/op")
		})
	}
}

func BenchmarkMatchRepeatedShortNoticeStages(b *testing.B) {
	matcher, err := New()
	if err != nil {
		b.Fatal(err)
	}
	input := []byte(strings.Repeat("mit license ", 15_000))
	tokens := matcher.engine.vocabulary.TokenizeIDs(input)
	candidates, method, err := matcher.engine.collectExactMatches(
		context.Background(),
		tokens.IDs,
	)
	if err != nil {
		b.Fatal(err)
	}
	filtered, err := filterExactMatches(
		context.Background(),
		matcher.engine,
		slices.Clone(candidates),
		allExactFilters,
	)
	if err != nil {
		b.Fatal(err)
	}
	offsets := tokenize.TokenOffsets(input, len(tokens.IDs))
	result := Result{Corpus: matcher.engine.info}
	if err := matcher.addExactMatches(
		context.Background(),
		input,
		&result,
		filtered,
		method,
		offsets,
		tokens.Start,
		tokens.End,
	); err != nil {
		b.Fatal(err)
	}

	b.Run("tokenization", func(b *testing.B) {
		b.SetBytes(int64(len(input)))
		for b.Loop() {
			benchmarkTokenIDs = matcher.engine.vocabulary.TokenizeIDs(input)
			benchmarkTokenOffsets = tokenize.TokenOffsets(input, len(benchmarkTokenIDs.IDs))
		}
	})
	b.Run("candidate-collection", func(b *testing.B) {
		for b.Loop() {
			benchmarkExactMatches, _, err = matcher.engine.collectExactMatches(
				context.Background(),
				tokens.IDs,
			)
			if err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(len(candidates)), "candidates/op")
	})
	b.Run("filtering", func(b *testing.B) {
		for b.Loop() {
			matches := slices.Clone(candidates)
			benchmarkExactMatches, err = filterExactMatches(
				context.Background(),
				matcher.engine,
				matches,
				allExactFilters,
			)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("match-construction", func(b *testing.B) {
		for b.Loop() {
			stageResult := Result{Corpus: matcher.engine.info}
			if err := matcher.addExactMatches(
				context.Background(),
				input,
				&stageResult,
				filtered,
				method,
				offsets,
				tokens.Start,
				tokens.End,
			); err != nil {
				b.Fatal(err)
			}
			benchmarkResult = stageResult
		}
	})
	b.Run("result-sorting", func(b *testing.B) {
		for b.Loop() {
			sortResult(&result)
		}
	})
}

func benchmarkRule(b *testing.B, id string) (*Matcher, []byte) {
	b.Helper()
	matcher, err := New()
	if err != nil {
		b.Fatal(err)
	}
	return matcher, embeddedRuleTexts(b, id)[id]
}

func newMatcher(index corpus.Index) (*Matcher, error) {
	engine, err := newMatchEngine(index)
	if err != nil {
		return nil, err
	}
	return &Matcher{engine: engine}, nil
}
