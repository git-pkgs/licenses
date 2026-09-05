package licenses

import "bytes"

func downgradeContinuedText(input []byte, result *Result) bool {
	changed := false
	detections := result.Detections[:0]
	for _, detection := range result.Detections {
		matches := detection.Matches[:0]
		for _, match := range detection.Matches {
			if match.Kind == KindText && continuesNumberedClause(input, match.Start, match.End) {
				match.Kind = KindClue
				result.Clues = append(result.Clues, match)
				changed = true
				continue
			}
			matches = append(matches, match)
		}
		detection.Matches = matches
		if len(matches) != 0 {
			detections = append(detections, detection)
		}
	}
	result.Detections = detections
	return changed
}

// Another numbered condition makes the matched prefix incomplete.
func continuesNumberedClause(input []byte, start, end int) bool {
	tail := bytes.TrimLeft(input[end:], " \t\r.*/")
	if len(tail) == 0 || tail[0] != '\n' {
		return false
	}
	next := clauseNumber(tail[1:])
	if next == 0 {
		return false
	}
	previous := 0
	for line := range bytes.SplitSeq(input[start:end], []byte{'\n'}) {
		if number := clauseNumber(line); number != 0 {
			previous = number
		}
	}
	return previous != 0 && next == previous+1
}

func clauseNumber(line []byte) int {
	line = bytes.TrimLeft(line, " \t\r\n*#/")
	const maxDigits = 3
	const decimalBase = 10
	number, digits := 0, 0
	for digits < len(line) && line[digits] >= '0' && line[digits] <= '9' {
		if digits == maxDigits {
			return 0
		}
		number = number*decimalBase + int(line[digits]-'0')
		digits++
	}
	if digits == 0 || digits+1 >= len(line) ||
		(line[digits] != '.' && line[digits] != ')') ||
		(line[digits+1] != ' ' && line[digits+1] != '\t') {
		return 0
	}
	return number
}
