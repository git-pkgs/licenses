package licenses

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestMatcherDenseSPDXDeclarations(t *testing.T) {
	m, err := New(WithMatchedText())
	if err != nil {
		t.Fatal(err)
	}
	block := "SPDX-License-Identifier: GPL-3.0-or-later\n" +
		"SPDX-License-Identifier: MIT OR ISC\n" +
		"/* SPDX-License-Identifier: eCos-2.0 */\n" +
		"SPDX-License-Identifier: LicenseRef-Probe\n"
	input := strings.Repeat(block, 1000) + "SPDX-License-Identifier: GPL-3.0-only\n"
	result, err := m.Match(context.Background(), []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]int{"GPL-3.0-or-later": 1000, "MIT OR ISC": 1000, "GPL-2.0-or-later WITH eCos-exception-2.0": 1000, "LicenseRef-scancode-unknown-spdx": 1000, "GPL-3.0-only": 1}
	if len(result.Detections) != len(expected) {
		t.Fatalf("detections = %d", len(result.Detections))
	}
	for _, d := range result.Detections {
		if len(d.Matches) != expected[d.Expression] {
			t.Fatalf("%s: %d matches, want %d", d.Expression, len(d.Matches), expected[d.Expression])
		}
		for _, match := range d.Matches {
			if string(match.Matched) != input[match.Start:match.End] {
				t.Fatalf("incorrect matched text: %+v", match)
			}
			if d.Expression == "GPL-3.0-only" && match.Start < 1000*len(block) {
				t.Fatal("subordinate prefix survived")
			}
		}
	}
}

func TestMatcherSPDXKeepsIndependentNotices(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		"MIT License\nSPDX-License-Identifier: MIT OR ISC\nISC License\n",
		"SPDX-License-Identifier: MIT OR ISC\nMIT License\nISC License\n",
	} {
		result, err := m.Match(context.Background(), []byte(input))
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, d := range result.Detections {
			got = append(got, d.Expression)
		}
		slices.Sort(got)
		if !slices.Equal(got, []string{"ISC", "MIT", "MIT OR ISC"}) {
			t.Fatalf("expressions: %v", got)
		}
	}
}

func BenchmarkMatcherDenseSPDX(b *testing.B) {
	m, err := New()
	if err != nil {
		b.Fatal(err)
	}
	for _, count := range []int{100, 1000, 10000} {
		input := []byte(strings.Repeat("SPDX-License-Identifier: GPL-3.0-or-later\n", count))
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			b.SetBytes(int64(len(input)))
			b.ReportAllocs()
			for b.Loop() {
				result, err := m.Match(context.Background(), input)
				if err != nil || len(result.Detections) != 1 || len(result.Detections[0].Matches) != count {
					b.Fatalf("incorrect dense result: %v", err)
				}
			}
		})
	}
}
