package licenses

import (
	pathpkg "path"
	"slices"
	"strings"
)

// Agreement states how a manifest declaration compares with the license text
// detected for the same package.
type Agreement string

const (
	// AgreementAgrees means the declaration and the package detections name
	// the same license and exception identifiers, treating -only and
	// -or-later variants of a license as the same license.
	AgreementAgrees Agreement = "agrees"
	// AgreementConflicts means the package detections name identifiers the
	// declaration does not, or the declaration names identifiers the
	// detections do not.
	AgreementConflicts Agreement = "conflicts"
	// AgreementUndetected means the declaration has an SPDX expression but no
	// license text was detected for the package.
	AgreementUndetected Agreement = "undetected"
	// AgreementUnnormalized means the declaration has no SPDX expression to
	// compare.
	AgreementUnnormalized Agreement = "unnormalized"
)

// resolveDeclarations compares each manifest declaration with the license
// text detected for its package. Package evidence is the license file the
// manifest names plus files with the license role in its directory. The
// manifest's own detections are excluded because they reread the declaration.
func resolveDeclarations(report *scanReport, scanned map[string]bool) {
	filesByDirectory := make(map[string][]fileRecord)
	filesByPath := make(map[string]fileRecord, len(report.Files))
	for _, file := range report.Files {
		directory := pathpkg.Dir(file.Path)
		filesByDirectory[directory] = append(filesByDirectory[directory], file)
		filesByPath[file.Path] = file
	}
	for index := range report.Declared {
		declared := &report.Declared[index]
		directory := pathpkg.Dir(declared.Path)
		declared.LicenseFilePath = resolveLicenseFile(directory, declared.LicenseFile, scanned)

		evidence := make(map[string]bool)
		addEvidence := func(file fileRecord) {
			for _, detection := range file.Detections {
				if detection.Identification != NoAssertion {
					evidence[detection.Expression] = true
				}
			}
		}
		for _, file := range filesByDirectory[directory] {
			if file.Path != declared.Path && file.Path != declared.LicenseFilePath &&
				slices.Contains(file.Roles, "license") {
				addEvidence(file)
			}
		}
		if file, ok := filesByPath[declared.LicenseFilePath]; ok && file.Path != declared.Path {
			addEvidence(file)
		}
		declared.Detected = make([]string, 0, len(evidence))
		for expression := range evidence {
			declared.Detected = append(declared.Detected, expression)
		}
		slices.Sort(declared.Detected)
		declared.Agreement = compareDeclaration(declared.NormalizedExpression, declared.Detected)

		report.Summary.FilesWithDeclarations++
		if declared.Agreement == AgreementConflicts {
			report.Summary.FilesWithConflictingDeclarations++
		}
	}
}

// resolveLicenseFile returns the scan-relative path of a license file named
// by a manifest in directory, or "" when that file was not scanned.
func resolveLicenseFile(directory, name string, scanned map[string]bool) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || pathpkg.IsAbs(name) {
		return ""
	}
	resolved := pathpkg.Join(directory, name)
	if resolved == ".." || strings.HasPrefix(resolved, "../") || !scanned[resolved] {
		return ""
	}
	return resolved
}

func compareDeclaration(expression string, detected []string) Agreement {
	if expression == "" {
		return AgreementUnnormalized
	}
	if len(detected) == 0 {
		return AgreementUndetected
	}
	detectedIDs := make([]string, 0)
	for _, value := range detected {
		detectedIDs = append(detectedIDs, expressionIdentifiers(value)...)
	}
	if slices.Equal(expressionIdentifiers(expression), slices.Compact(sortedFolded(detectedIDs))) {
		return AgreementAgrees
	}
	return AgreementConflicts
}

// expressionIdentifiers returns the sorted license and exception identifiers
// in an SPDX expression in comparable form, ignoring its operators.
func expressionIdentifiers(expression string) []string {
	fields := strings.Fields(strings.NewReplacer("(", " ", ")", " ").Replace(expression))
	identifiers := make([]string, 0, len(fields))
	for _, field := range fields {
		switch strings.ToUpper(field) {
		case "AND", "OR", "WITH":
			continue
		}
		identifiers = append(identifiers, field)
	}
	return slices.Compact(sortedFolded(identifiers))
}

// sortedFolded case-folds and sorts identifiers. It also drops -only,
// -or-later and + suffixes, since license text cannot say whether later
// versions apply and is detected as the -only variant.
func sortedFolded(values []string) []string {
	for index, value := range values {
		value = strings.ToLower(value)
		value = strings.TrimSuffix(value, "+")
		value = strings.TrimSuffix(value, "-or-later")
		value = strings.TrimSuffix(value, "-only")
		values[index] = value
	}
	slices.Sort(values)
	return values
}
