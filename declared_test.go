package licenses

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestScanRepositoryResolvesDeclarations(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	license := projectLicense(t)
	writeTestFile(t, filepath.Join(root, "LICENSE"), license)
	writeTestFile(t, filepath.Join(root, "package.json"), []byte(`{"name":"root","license":"MIT"}`))
	writeTestFile(t, filepath.Join(root, "NOTICE"), []byte("Bundled code:\nSPDX-License-Identifier: BSD-3-Clause\n"))
	writeTestFile(t, filepath.Join(root, "README.md"), []byte("Docs are licensed under:\nSPDX-License-Identifier: CC-BY-4.0\n"))
	writeTestFile(t, filepath.Join(root, "sub", "package.json"), []byte(`{"name":"sub","license":"PSF"}`))
	writeTestFile(t, filepath.Join(root, "lone", "package.json"), []byte(`{"name":"lone","license":"MIT"}`))
	writeTestFile(t, filepath.Join(root, "pod", "Example.podspec"), []byte(`Pod::Spec.new do |s|
  s.name = "Example"
  s.version = "1.0.0"
  s.license = "BSD"
end
`))
	writeTestFile(t, filepath.Join(root, "gpl", "package.json"), []byte(`{"name":"gpl","license":"GPL-3.0-or-later"}`))
	writeTestFile(t, filepath.Join(root, "gpl", "COPYING"), []byte("SPDX-License-Identifier: GPL-3.0-only\n"))
	writeTestFile(t, filepath.Join(root, "conflict", "package.json"), []byte(`{"name":"conflict","license":"MIT"}`))
	writeTestFile(t, filepath.Join(root, "conflict", "LICENSE"), []byte("SPDX-License-Identifier: Apache-2.0\n"))
	writeTestFile(t, filepath.Join(root, "cargo", "Cargo.toml"), []byte(`[package]
name = "license-file"
version = "0.1.0"
license-file = "docs/COPYING"
`))
	writeTestFile(t, filepath.Join(root, "cargo", "docs", "COPYING"), license)
	writeTestFile(t, filepath.Join(root, "missing", "Cargo.toml"), []byte(`[package]
name = "missing"
version = "0.1.0"
license-file = "LICENSE.custom"
`))

	report, err := scanRepository(
		context.Background(),
		newTestMatcher(t),
		root,
		defaultTestScanOptions(),
		testScannerVersion,
	)
	if err != nil {
		t.Fatal(err)
	}
	for path, expression := range map[string]string{"NOTICE": "BSD-3-Clause", "README.md": "CC-BY-4.0"} {
		index := slices.IndexFunc(report.Files, func(file fileRecord) bool { return file.Path == path })
		if index < 0 || !slices.ContainsFunc(report.Files[index].Detections, func(detection detectionRecord) bool {
			return detection.Expression == expression
		}) {
			t.Fatalf("%s has no %s detection to exclude: %+v", path, expression, report.Files)
		}
	}
	type resolved struct {
		path            string
		licenseFilePath string
		detected        []string
		agreement       Agreement
	}
	want := []resolved{
		{path: "cargo/Cargo.toml", licenseFilePath: "cargo/docs/COPYING", detected: []string{"MIT"}, agreement: AgreementUnnormalized},
		{path: "conflict/package.json", detected: []string{"Apache-2.0"}, agreement: AgreementConflicts},
		{path: "gpl/package.json", detected: []string{"GPL-3.0-only"}, agreement: AgreementAgrees},
		{path: "lone/package.json", detected: []string{}, agreement: AgreementUndetected},
		{path: "missing/Cargo.toml", detected: []string{}, agreement: AgreementUnnormalized},
		{path: "package.json", detected: []string{"MIT"}, agreement: AgreementAgrees},
		{path: "pod/Example.podspec", detected: []string{}, agreement: AgreementUndetected},
		{path: "sub/package.json", detected: []string{}, agreement: AgreementUndetected},
	}
	got := make([]resolved, 0, len(report.Declared))
	for _, record := range report.Declared {
		got = append(got, resolved{
			path:            record.Path,
			licenseFilePath: record.LicenseFilePath,
			detected:        record.Detected,
			agreement:       record.Agreement,
		})
	}
	if !slices.EqualFunc(got, want, func(got, want resolved) bool {
		return got.path == want.path &&
			got.licenseFilePath == want.licenseFilePath &&
			slices.Equal(got.detected, want.detected) &&
			got.agreement == want.agreement
	}) {
		t.Errorf("declarations = %+v, want %+v", got, want)
	}
	if report.Summary.FilesWithDeclarations != 8 || report.Summary.FilesWithConflictingDeclarations != 1 {
		t.Errorf("summary = %+v, want 8 declarations and 1 conflict", report.Summary)
	}

	encoded, err := json.Marshal(report.Declared)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`"path":"missing/Cargo.toml","raw":[],"license_file":"LICENSE.custom","normalized_expression":"","license_file_path":"","detected":[],"agreement":"unnormalized"`,
		`"agreement":"conflicts"`,
	} {
		if !strings.Contains(string(encoded), field) {
			t.Errorf("declared JSON does not contain %s:\n%s", field, encoded)
		}
	}
	if strings.Contains(string(encoded), `"root"`) {
		t.Errorf("declared JSON contains a root field:\n%s", encoded)
	}
}

func TestScanRepositoryDeclarationEvidenceStaysInPackage(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "LICENSE"), projectLicense(t))
	writeTestFile(t, filepath.Join(root, "src", "main.go"), []byte("// SPDX-License-Identifier: Apache-2.0\npackage main\n"))
	writeTestFile(t, filepath.Join(root, "nested", "package.json"), []byte(`{"name":"nested","license":"ISC"}`))
	writeTestFile(t, filepath.Join(root, "Cargo.toml"), []byte(`[package]
name = "root"
version = "0.1.0"
license = "MIT"
license-file = "../outside/LICENSE"
`))

	report, err := scanRepository(
		context.Background(),
		newTestMatcher(t),
		root,
		defaultTestScanOptions(),
		testScannerVersion,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Declared) != 2 {
		t.Fatalf("declared = %+v, want root and nested manifests", report.Declared)
	}
	rootManifest, nested := report.Declared[0], report.Declared[1]
	if rootManifest.Path != "Cargo.toml" || !slices.Equal(rootManifest.Detected, []string{"MIT"}) ||
		rootManifest.Agreement != AgreementAgrees || rootManifest.LicenseFilePath != "" {
		t.Errorf("root declaration = %+v, want MIT from LICENSE only", rootManifest)
	}
	if nested.Path != "nested/package.json" || len(nested.Detected) != 0 ||
		nested.Agreement != AgreementUndetected {
		t.Errorf("nested declaration = %+v, want undetected without the root LICENSE", nested)
	}
}

func TestScanRepositoryNamedNoticeFileIsEvidence(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "NOTICE"), projectLicense(t))
	writeTestFile(t, filepath.Join(root, "Cargo.toml"), []byte(`[package]
name = "notice"
version = "0.1.0"
license = "MIT"
license-file = "NOTICE"
`))

	report, err := scanRepository(
		context.Background(),
		newTestMatcher(t),
		root,
		defaultTestScanOptions(),
		testScannerVersion,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Declared) != 1 || report.Declared[0].LicenseFilePath != "NOTICE" ||
		!slices.Equal(report.Declared[0].Detected, []string{"MIT"}) ||
		report.Declared[0].Agreement != AgreementAgrees {
		t.Errorf("declared = %+v, want MIT from the named NOTICE file", report.Declared)
	}
}

func TestScanRepositoryUnscannedLicenseFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "LICENSE"), projectLicense(t))
	writeTestFile(t, filepath.Join(root, "packages", "foo", "Cargo.toml"), []byte(`[package]
name = "foo"
version = "0.1.0"
license-file = "LICENSE"
`))
	if err := os.Symlink(filepath.Join("..", "..", "LICENSE"), filepath.Join(root, "packages", "foo", "LICENSE")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	report, err := scanRepository(
		context.Background(),
		newTestMatcher(t),
		root,
		defaultTestScanOptions(),
		testScannerVersion,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Declared) != 1 || report.Declared[0].LicenseFilePath != "" ||
		report.Declared[0].Agreement != AgreementUnnormalized {
		t.Errorf("declared = %+v, want a symlinked license file left unresolved", report.Declared)
	}
}

func TestResolveLicenseFile(t *testing.T) {
	t.Parallel()

	discovered := map[string]bool{
		"LICENSE":            true,
		"pkg/COPYING":        true,
		"pkg/docs/LICENSE":   true,
		"pkg/LICENSE.custom": true,
	}
	for _, test := range []struct {
		directory string
		name      string
		want      string
	}{
		{directory: "pkg", name: "COPYING", want: "pkg/COPYING"},
		{directory: "pkg", name: " ./docs/LICENSE ", want: "pkg/docs/LICENSE"},
		{directory: "pkg", name: `docs\LICENSE`, want: "pkg/docs/LICENSE"},
		{directory: "pkg", name: "../LICENSE", want: "LICENSE"},
		{directory: ".", name: "LICENSE", want: "LICENSE"},
		{directory: ".", name: "../LICENSE", want: ""},
		{directory: "pkg", name: "/etc/passwd", want: ""},
		{directory: "pkg", name: "MISSING", want: ""},
		{directory: "pkg", name: "", want: ""},
	} {
		if got := resolveLicenseFile(test.directory, test.name, discovered); got != test.want {
			t.Errorf("resolveLicenseFile(%q, %q) = %q, want %q", test.directory, test.name, got, test.want)
		}
	}
}

func TestCompareDeclaration(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		expression string
		detected   []string
		want       Agreement
	}{
		{expression: "MIT", detected: []string{"MIT"}, want: AgreementAgrees},
		{expression: "MIT OR Apache-2.0", detected: []string{"Apache-2.0", "MIT"}, want: AgreementAgrees},
		{expression: "MIT OR Apache-2.0", detected: []string{"MIT OR Apache-2.0"}, want: AgreementAgrees},
		{expression: "Apache-2.0 WITH LLVM-exception", detected: []string{"Apache-2.0", "LLVM-exception"}, want: AgreementAgrees},
		{expression: "(MIT AND ISC)", detected: []string{"isc", "mit"}, want: AgreementAgrees},
		{expression: "BSD-2-Clause", detected: []string{"BSD-3-Clause"}, want: AgreementConflicts},
		{expression: "GPL-3.0-or-later", detected: []string{"GPL-3.0-only"}, want: AgreementAgrees},
		{expression: "LGPL-2.1-or-later", detected: []string{"LGPL-2.1-only"}, want: AgreementAgrees},
		{expression: "AGPL-3.0-only", detected: []string{"AGPL-3.0-or-later"}, want: AgreementAgrees},
		{expression: "GPL-2.0+", detected: []string{"GPL-2.0-only"}, want: AgreementAgrees},
		{expression: "GPL-2.0-or-later OR MIT", detected: []string{"GPL-2.0-only", "MIT"}, want: AgreementAgrees},
		{expression: "GPL-2.0-or-later", detected: []string{"GPL-3.0-only"}, want: AgreementConflicts},
		{expression: "LGPL-2.1-or-later", detected: []string{"GPL-2.0-only"}, want: AgreementConflicts},
		{expression: "GPL-3.0-or-later", detected: []string{"GPL-3.0-only", "LicenseRef-scancode-unknown-license-reference"}, want: AgreementConflicts},
		{expression: "MIT OR Apache-2.0", detected: []string{"MIT"}, want: AgreementConflicts},
		{expression: "MIT", detected: []string{"MIT", "Apache-2.0"}, want: AgreementConflicts},
		{expression: "PSF-2.0", detected: []string{}, want: AgreementUndetected},
		{expression: "", detected: []string{"MIT"}, want: AgreementUnnormalized},
	} {
		if got := compareDeclaration(test.expression, slices.Clone(test.detected)); got != test.want {
			t.Errorf("compareDeclaration(%q, %q) = %q, want %q", test.expression, test.detected, got, test.want)
		}
	}
}
