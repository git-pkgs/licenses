package licenses

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestMatcherContinuedConditions(t *testing.T) {
	input, err := os.ReadFile("testdata/software-heritage/bsd-three-conditions.txt")
	if err != nil {
		t.Fatal(err)
	}
	end := strings.Index(string(input), "3.")
	if end < 0 {
		t.Fatal("missing third condition")
	}
	m, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		text string
		want bool
	}{
		{"continued", string(input), false},
		{"ends at second condition", string(input[:end]), true},
		{"separate numbered list", string(input[:end]) + "Unrelated components:\n3. Another component", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := m.Match(context.Background(), []byte(test.text))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, detection := range result.Detections {
				found = found || detection.Expression == "BSD-2-Clause"
			}
			if found != test.want {
				t.Fatalf("BSD-2-Clause detection = %v, want %v: %+v", found, test.want, result)
			}
		})
	}
}

func TestMatcherFoundationAttribution(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"software developed by The Apache Software Foundation",
		"includes free software developed by the Apache Software Foundation",
	} {
		result, err := m.Match(context.Background(), []byte(text))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Detections) != 0 || len(result.Clues) != 1 {
			t.Fatalf("attribution %q: %+v", text, result)
		}
	}
}

func BenchmarkMatcherSingleLineNotices(b *testing.B) {
	text, err := os.ReadFile("LICENSE")
	if err != nil {
		b.Fatal(err)
	}
	input := []byte(strings.Repeat(strings.ReplaceAll(string(text), "\n", " "), 2000))
	matcher, err := New()
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(input)))
	b.ReportAllocs()
	for b.Loop() {
		result, err := matcher.Match(context.Background(), input)
		if err != nil || len(result.Detections) == 0 {
			b.Fatalf("match failed: %v", err)
		}
	}
}
