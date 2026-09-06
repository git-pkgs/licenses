package licenses_test

import (
	"context"
	"testing"

	"github.com/git-pkgs/licenses"
)

func TestMatchHeaderAfterUnicodeAndMalformedText(t *testing.T) {
	m, err := licenses.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"/* Café ΔΙΚΑΙΩΜΑ １２３ */\n", "// invalid: \xff\xc0\n", "// GPL++ punctuation\n"} {
		input := prefix + "// SPDX-License-Identifier: Apache-2.0\npackage example\n"
		result, err := m.Match(context.Background(), []byte(input))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, detection := range result.Detections {
			if detection.Expression != "Apache-2.0" {
				continue
			}
			found = true
			for _, match := range detection.Matches {
				if match.Start < len(prefix) || match.End > len(input) || match.End <= match.Start {
					t.Fatalf("prefix %q: incorrect byte span %d-%d", prefix, match.Start, match.End)
				}
			}
		}
		if !found {
			t.Fatalf("prefix %q: no Apache-2.0 detection", prefix)
		}
	}
}
