package licenses

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestMatcherSparseNoticeOffsets(t *testing.T) {
	matcher, err := New(WithMatchedText())
	if err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("qzxw éééé ", 100_000) + "MIT License\n"
	for _, input := range []string{large, "MIT License", large} {
		result, err := matcher.Match(context.Background(), []byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Detections) != 1 || result.Detections[0].Expression != "MIT" || len(result.Detections[0].Matches) != 1 {
			t.Fatalf("unexpected detections: %+v", result.Detections)
		}
		match := result.Detections[0].Matches[0]
		start := strings.LastIndex(input, "MIT License")
		if match.Start != start || match.End != start+len("MIT License") || string(match.Matched) != "MIT License" {
			t.Fatalf("incorrect byte range: %+v", match)
		}
	}
}

func BenchmarkMatcherSparseNotice(b *testing.B) {
	matcher, err := New()
	if err != nil {
		b.Fatal(err)
	}
	for _, size := range []int{1 << 20, 16 << 20, 64 << 20} {
		input := []byte(strings.Repeat("qzxw ", (size+4)/5)[:size-len(" MIT License")] + " MIT License")
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			b.SetBytes(int64(len(input)))
			b.ReportAllocs()
			for b.Loop() {
				result, err := matcher.Match(context.Background(), input)
				if err != nil || len(result.Detections) != 1 || result.Detections[0].Expression != "MIT" {
					b.Fatalf("incorrect sparse result: %v", err)
				}
			}
		})
	}
}

func BenchmarkMatcherScratchRetention(b *testing.B) {
	matcher, err := New()
	if err != nil {
		b.Fatal(err)
	}
	for _, count := range []int{55296, 55297, 65536, 65537, 131072} {
		input := []byte(strings.Repeat("qzxw ", count))
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			b.SetBytes(int64(len(input)))
			b.ReportAllocs()
			for b.Loop() {
				result, err := matcher.Match(context.Background(), input)
				if err != nil || len(result.Detections) != 0 {
					b.Fatalf("incorrect unmatched result: %v", err)
				}
			}
		})
	}
	b.Run("large-then-small", func(b *testing.B) {
		large := []byte(strings.Repeat("qzxw ", 131072))
		small := []byte(strings.Repeat("qzxw ", 100))
		b.SetBytes(int64(len(large) + len(small)))
		b.ReportAllocs()
		for b.Loop() {
			for _, input := range [][]byte{large, small} {
				result, err := matcher.Match(context.Background(), input)
				if err != nil || len(result.Detections) != 0 {
					b.Fatalf("incorrect unmatched result: %v", err)
				}
			}
		}
	})
}
