package main

import (
	"os"
	"path/filepath"
	"testing"
)

const testScannerVersion = "test-version"

func projectLicense(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func hasMITExpression(file fileRecord) bool {
	for _, detection := range file.Detections {
		if detection.Expression == "MIT" {
			return true
		}
	}
	return false
}

func hasSkip(records []skipRecord, want skipRecord) bool {
	for _, record := range records {
		if record == want {
			return true
		}
	}
	return false
}

func findRecordMatch(file fileRecord, ruleID string) (matchRecord, bool) {
	for _, detection := range file.Detections {
		for _, match := range detection.Matches {
			if match.RuleID == ruleID {
				return match, true
			}
		}
	}
	return matchRecord{}, false
}
