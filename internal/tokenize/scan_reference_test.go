package tokenize

import (
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func FuzzWordsAgainstRuneScanner(f *testing.F) {
	for _, input := range []string{"", "SPDX-License-Identifier: Apache-2.0", "GPL+2 gpl++3 +alone", "Café ΔΙΚΑΙΩΜΑ １２３", "MIT\xffGPL\xc0"} {
		f.Add([]byte(input))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		var want []Word
		for offset := 0; offset < len(input); {
			r, width := utf8.DecodeRune(input[offset:])
			if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
				offset += width
				continue
			}
			start := offset
			offset += width
			plus := false
			for offset < len(input) {
				r, width = utf8.DecodeRune(input[offset:])
				if unicode.IsLetter(r) || unicode.IsNumber(r) {
					offset += width
					continue
				}
				if r == '+' && !plus {
					plus = true
					offset += width
					continue
				}
				break
			}
			want = append(want, Word{Text: strings.ToLower(string(input[start:offset])), Start: start, End: offset})
		}
		if got := Words(input); !slices.Equal(got, want) {
			t.Fatalf("Words(%q) = %#v, want %#v", input, got, want)
		}
	})
}
