package licenses

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/git-pkgs/licenses/internal/corpus"
	"github.com/git-pkgs/licenses/internal/tokenize"
)

var (
	benchmarkMatcher *Matcher
	benchmarkResult  Result
)

func BenchmarkMatcherColdStart(b *testing.B) {
	for b.Loop() {
		index, err := corpus.Load()
		if err != nil {
			b.Fatal(err)
		}
		benchmarkMatcher, err = newMatcher(index)
		if err != nil {
			b.Fatal(err)
		}
	}
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
	inputs := make([][]byte, 0, len(matcher.engine.rules))
	var byteCount int64
	for _, rule := range matcher.engine.rules {
		if len(rule.Tokens) == 0 {
			continue
		}
		input := normalizedRuleText(matcher.engine, rule)
		inputs = append(inputs, input)
		byteCount += int64(len(input))
	}
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

func BenchmarkMatchRepeatedShortNotice(b *testing.B) {
	matcher, err := New()
	if err != nil {
		b.Fatal(err)
	}
	for _, repetitions := range []int{5_000, 10_000, 15_000} {
		input := []byte(strings.Repeat("mit license ", repetitions))
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

func benchmarkRule(b *testing.B, id string) (*Matcher, []byte) {
	b.Helper()
	matcher, err := New()
	if err != nil {
		b.Fatal(err)
	}
	for _, rule := range matcher.engine.rules {
		if rule.ID == id {
			return matcher, normalizedRuleText(matcher.engine, rule)
		}
	}
	b.Fatalf("%s is absent", id)
	return nil, nil
}

func normalizedRuleText(engine *matchEngine, rule corpus.Rule) []byte {
	words := make([]string, len(rule.Tokens))
	for index, token := range rule.Tokens {
		words[index] = engine.vocabulary.Word(tokenize.ID(token))
	}
	return []byte(strings.Join(words, " "))
}

func newMatcher(index corpus.Index) (*Matcher, error) {
	engine, err := newMatchEngine(index)
	if err != nil {
		return nil, err
	}
	return &Matcher{engine: engine}, nil
}
