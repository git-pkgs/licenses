package licenses_test

import (
	"context"
	"strings"
	"testing"

	"github.com/git-pkgs/licenses"
)

func TestSPDXDeclarationCoveredByCorpusRule(t *testing.T) {
	t.Parallel()
	matcher, err := licenses.New()
	if err != nil {
		t.Fatal(err)
	}
	tag := "SPDX-License-Identifier: GPL-2.0"
	input := "// " + tag + "\nint example(void) { return 0; }\n"
	result, err := matcher.Match(context.Background(), []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SPDXDeclarations) != 1 {
		t.Fatalf("declarations = %#v, want one", result.SPDXDeclarations)
	}
	declaration := result.SPDXDeclarations[0]
	if declaration.Expression != "GPL-2.0-only" || input[declaration.Start:declaration.End] != tag {
		t.Fatalf("declaration = %#v", declaration)
	}
	if len(result.Detections) != 1 || result.Detections[0].Expression != declaration.Expression {
		t.Fatalf("detections changed: %#v", result.Detections)
	}
	for _, match := range result.Detections[0].Matches {
		if match.Method == licenses.SpdxID {
			t.Fatalf("redundant tag detection survived overlap: %#v", match)
		}
	}
}

func TestSPDXDeclarationsPreserveOccurrences(t *testing.T) {
	t.Parallel()
	matcher, err := licenses.New()
	if err != nil {
		t.Fatal(err)
	}
	input := "// SPDX-License-Identifier: MIT\n/* SPDX-License-Identifier: MIT */\n"
	result, err := matcher.Match(context.Background(), []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SPDXDeclarations) != 2 {
		t.Fatalf("declarations = %#v, want two occurrences", result.SPDXDeclarations)
	}
	for _, declaration := range result.SPDXDeclarations {
		if declaration.Expression != "MIT" || input[declaration.Start:declaration.End] != "SPDX-License-Identifier: MIT" {
			t.Fatalf("declaration = %#v", declaration)
		}
	}
	if result.SPDXDeclarations[0].End >= result.SPDXDeclarations[1].Start {
		t.Fatalf("declarations are not in input order: %#v", result.SPDXDeclarations)
	}
	if len(result.Detections) != 1 {
		t.Fatalf("repeated tags must not duplicate expressions: %#v", result.Detections)
	}
}

func TestSPDXDeclarationsRequireParsedTags(t *testing.T) {
	t.Parallel()
	matcher, err := licenses.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		"Released under the MIT license.",
		"// SPDX-License-Identifier: MIT AND\n",
		"// SPDX-License-Identifier: NoSuchLicense-1.0\n",
		"// SPDX-License-Identifier:\n",
	} {
		t.Run(strings.TrimSpace(input), func(t *testing.T) {
			result, err := matcher.Match(context.Background(), []byte(input))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.SPDXDeclarations) != 0 {
				t.Fatalf("unexpected declarations: %#v", result.SPDXDeclarations)
			}
		})
	}
}
