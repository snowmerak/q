package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ReadFileTool reads a file and returns prefixed output
func ReadFileTool(filePath string, startLine, endLine int, maxLines int) (string, int, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", 0, err
	}

	content := string(data)
	lines := strings.Split(content, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	totalLines := len(lines)

	if startLine < 1 {
		startLine = 1
	}
	if endLine <= 0 || endLine > totalLines {
		endLine = totalLines
	}
	if endLine-startLine+1 > maxLines {
		endLine = startLine + maxLines - 1
	}
	if startLine > endLine {
		return "", totalLines, fmt.Errorf("start_line (%d) cannot be greater than end_line (%d)", startLine, endLine)
	}

	slice := lines[startLine-1 : endLine]
	formatted := FormatReadOutput(slice, startLine)
	return strings.Join(formatted, "\n"), totalLines, nil
}

// EditFileTool atomically replaces a range of lines checking pos and end anchors
func EditFileTool(filePath string, posStr, endStr, replacement string) (string, error) {
	pos, err := ParseAnchor(posStr)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}

	if pos.Line < 1 || pos.Line > len(lines) {
		return "", fmt.Errorf("[E_INVALID_PATCH] pos line %d out of bounds", pos.Line)
	}

	// Validate pos hash
	actualPosContent := lines[pos.Line-1]
	actualPosHash := HashLine(actualPosContent, pos.Line)
	if actualPosHash != pos.Hash {
		return "", fmt.Errorf("[E_STALE_CONTEXT] pos line %d changed: is now \"%s\" (correct anchor: %d#%s)", pos.Line, actualPosContent, pos.Line, actualPosHash)
	}

	endLine := pos.Line
	if endStr != "" && endStr != posStr {
		end, err := ParseAnchor(endStr)
		if err != nil {
			return "", err
		}
		if end.Line < pos.Line || end.Line > len(lines) {
			return "", fmt.Errorf("[E_INVALID_PATCH] end line %d invalid", end.Line)
		}

		actualEndContent := lines[end.Line-1]
		actualEndHash := HashLine(actualEndContent, end.Line)
		if actualEndHash != end.Hash {
			return "", fmt.Errorf("[E_STALE_CONTEXT] end line %d changed: is now \"%s\" (correct anchor: %d#%s)", end.Line, actualEndContent, end.Line, actualEndHash)
		}
		endLine = end.Line
	}

	// Ensure replacement doesn't contain anchors
	if regexp.MustCompile(`(^\d+#[A-Z]{2}:)`).MatchString(replacement) {
		return "", fmt.Errorf("[E_INVALID_PATCH] Replacement data contains anchor prefixes. Plain text only")
	}

	replacement = strings.ReplaceAll(replacement, "\r\n", "\n")
	newLines := strings.Split(replacement, "\n")
	for i := range newLines {
		newLines[i] = strings.TrimSuffix(newLines[i], "\r")
	}

	// Diff preview
	oldBlock := lines[pos.Line-1 : endLine]
	oldHashes := make([]string, len(oldBlock))
	for i, l := range oldBlock {
		oldHashes[i] = HashLine(l, pos.Line+i)
	}

	newHashes := make([]string, len(newLines))
	for i, l := range newLines {
		newHashes[i] = HashLine(l, pos.Line+i)
	}
	diffPreview := GenerateDiffPreview(oldBlock, newLines, pos.Line, oldHashes, newHashes)

	// Splice lines
	prefix := lines[:pos.Line-1]
	suffix := lines[endLine:]

	resultLines := make([]string, 0, len(prefix)+len(newLines)+len(suffix))
	resultLines = append(resultLines, prefix...)
	resultLines = append(resultLines, newLines...)
	resultLines = append(resultLines, suffix...)

	// Create directories if needed
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return "", err
	}

	err = os.WriteFile(filePath, []byte(strings.Join(resultLines, "\n")), 0644)
	if err != nil {
		return "", err
	}
	return diffPreview, nil
}

// InsertFileTool inserts text after pos anchor
func InsertFileTool(filePath string, posStr, content string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}

	insertIdx := 0
	if posStr != "0" {
		pos, err := ParseAnchor(posStr)
		if err != nil {
			return err
		}
		if pos.Line < 1 || pos.Line > len(lines) {
			return fmt.Errorf("[E_INVALID_PATCH] pos line %d out of bounds", pos.Line)
		}
		actualContent := lines[pos.Line-1]
		actualHash := HashLine(actualContent, pos.Line)
		if actualHash != pos.Hash {
			return fmt.Errorf("[E_STALE_CONTEXT] pos line %d changed: is now \"%s\" (correct anchor: %d#%s)", pos.Line, actualContent, pos.Line, actualHash)
		}
		insertIdx = pos.Line
	}

	content = strings.ReplaceAll(content, "\r\n", "\n")
	newLines := strings.Split(content, "\n")
	for i := range newLines {
		newLines[i] = strings.TrimSuffix(newLines[i], "\r")
	}

	prefix := lines[:insertIdx]
	suffix := lines[insertIdx:]

	resultLines := make([]string, 0, len(prefix)+len(newLines)+len(suffix))
	resultLines = append(resultLines, prefix...)
	resultLines = append(resultLines, newLines...)
	resultLines = append(resultLines, suffix...)

	return os.WriteFile(filePath, []byte(strings.Join(resultLines, "\n")), 0644)
}

// EraseFileTool deletes lines from pos to end inclusive
func EraseFileTool(filePath string, posStr, endStr string) error {
	pos, err := ParseAnchor(posStr)
	if err != nil {
		return err
	}

	end, err := ParseAnchor(endStr)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}

	if pos.Line < 1 || pos.Line > len(lines) || end.Line < pos.Line || end.Line > len(lines) {
		return fmt.Errorf("[E_INVALID_PATCH] range %d-%d out of bounds", pos.Line, end.Line)
	}

	// Validate hashes
	actualPos := lines[pos.Line-1]
	actualPosHash := HashLine(actualPos, pos.Line)
	if actualPosHash != pos.Hash {
		return fmt.Errorf("[E_STALE_CONTEXT] pos line %d changed (correct anchor: %d#%s)", pos.Line, pos.Line, actualPosHash)
	}

	actualEnd := lines[end.Line-1]
	actualEndHash := HashLine(actualEnd, end.Line)
	if actualEndHash != end.Hash {
		return fmt.Errorf("[E_STALE_CONTEXT] end line %d changed (correct anchor: %d#%s)", end.Line, end.Line, actualEndHash)
	}

	prefix := lines[:pos.Line-1]
	suffix := lines[end.Line:]

	resultLines := make([]string, 0, len(prefix)+len(suffix))
	resultLines = append(resultLines, prefix...)
	resultLines = append(resultLines, suffix...)

	return os.WriteFile(filePath, []byte(strings.Join(resultLines, "\n")), 0644)
}
