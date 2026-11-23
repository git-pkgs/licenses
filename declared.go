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
	// AgreementAgrees means every declared license has matching detected
	// text. The -only and -or-later variants of a license count as the same
	// license, WITH exceptions need no text and extra detections are allowed.
	AgreementAgrees Agreement = "agrees"
	// AgreementConflicts means a declared license has no matching detected
	// text.
	AgreementConflicts Agreement = "conflicts"
	// AgreementUndetected means the declaration has an SPDX expression but no
	// license text was detected for the package.
	AgreementUndetected Agreement = "undetected"
	// AgreementUnnormalized means the declaration has no SPDX expression to
	// compare.
	AgreementUnnormalized Agreement = "unnormalized"
)

// resolveDeclarations compares each declaration with its package's license files.
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
	found := make(map[string]bool)
	for _, value := range detected {
		for _, identifier := range licenseIdentifiers(value) {
			found[identifier] = true
		}
	}
	for _, identifier := range licenseIdentifiers(expression) {
		if !found[identifier] {
			return AgreementConflicts
		}
	}
	return AgreementAgrees
}

// licenseIdentifiers returns the comparable license identifiers in an SPDX
// expression, without operators or WITH exceptions.
func licenseIdentifiers(expression string) []string {
	fields := strings.Fields(strings.NewReplacer("(", " ", ")", " ").Replace(expression))
	identifiers := make([]string, 0, len(fields))
	for index := 0; index < len(fields); index++ {
		switch strings.ToUpper(fields[index]) {
		case "AND", "OR":
			continue
		case "WITH":
			index++
			continue
		}
		identifiers = append(identifiers, comparableIdentifier(fields[index]))
	}
	return identifiers
}

// comparableIdentifier folds case and the -only, -or-later and + variants,
// since license text is detected as the -only variant.
func comparableIdentifier(identifier string) string {
	identifier = strings.ToLower(identifier)
	identifier = strings.TrimSuffix(identifier, "+")
	identifier = strings.TrimSuffix(identifier, "-or-later")
	return strings.TrimSuffix(identifier, "-only")
}
