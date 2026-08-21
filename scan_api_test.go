package licenses_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	licenses "github.com/git-pkgs/licenses"
)

func TestScanRepositoryPublicAPI(t *testing.T) {
	t.Parallel()

	matcher, err := licenses.New()
	if err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile("LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "LICENSE"), text, 0o600); err != nil {
		t.Fatal(err)
	}

	options := licenses.DefaultScanOptions()
	options.ScannerVersion = "consumer-version"
	report, err := licenses.ScanRepository(
		context.Background(),
		matcher,
		root,
		options,
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Schema != licenses.ReportSchemaVersion {
		t.Errorf("schema = %d, want %d", report.Schema, licenses.ReportSchemaVersion)
	}
	if report.Scanner != (licenses.ScannerRecord{
		Name:    licenses.ScannerName,
		Version: "consumer-version",
	}) {
		t.Errorf("scanner = %#v", report.Scanner)
	}
	if len(report.Files) != 1 {
		t.Fatalf("files = %#v, want one", report.Files)
	}
	file := report.Files[0]
	if file.Path != "LICENSE" || file.SHA256 == "" || len(file.Roles) != 1 || file.Roles[0] != "license" {
		t.Errorf("file = %#v, want hashed license file", file)
	}
	if file.Text != "" {
		t.Errorf("text = %q, want empty without IncludeLegalFiles", file.Text)
	}
}

func TestScanRepositoryPublicValidation(t *testing.T) {
	t.Parallel()

	options := licenses.DefaultScanOptions()
	options.Workers = 0
	if err := licenses.ValidateScanOptions(options); err == nil {
		t.Fatal("ValidateScanOptions returned nil for zero workers")
	}
	if roles := licenses.LegalFileRoles("legal/LICENSE.txt"); len(roles) != 1 || roles[0] != "license" {
		t.Fatalf("LegalFileRoles = %#v, want license", roles)
	}
}

func TestScanRepositoryIncludesUnmatchedLegalFiles(t *testing.T) {
	t.Parallel()

	matcher, err := licenses.New()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	files := []struct {
		path    string
		content []byte
		role    string
	}{
		{path: "LICENSE.custom", content: []byte("ZQXWV-184729\n"), role: "license"},
		{path: "NOTICE.custom", content: []byte("QAZWSX-593821\n"), role: "notice"},
	}
	for _, file := range files {
		if err := os.WriteFile(filepath.Join(root, file.path), file.content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "ordinary.txt"), []byte("MNBVCX-274610\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	options := licenses.DefaultScanOptions()
	report, err := licenses.ScanRepository(context.Background(), matcher, root, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Files) != 0 {
		t.Fatalf("default files = %#v, want none", report.Files)
	}

	options.IncludeLegalFiles = true
	report, err = licenses.ScanRepository(context.Background(), matcher, root, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Files) != len(files) {
		t.Fatalf("files = %#v, want %d", report.Files, len(files))
	}
	for index, want := range files {
		file := report.Files[index]
		wantSHA256 := sha256.Sum256(want.content)
		if file.Path != want.path || file.Size != int64(len(want.content)) ||
			file.SHA256 != fmt.Sprintf("%x", wantSHA256) || file.Encoding != "utf-8" ||
			file.Text != string(want.content) ||
			len(file.Roles) != 1 || file.Roles[0] != want.role ||
			len(file.Detections) != 0 || len(file.Clues) != 0 {
			t.Errorf("file = %#v, want unmatched %s metadata", file, want.role)
		}
	}
}
