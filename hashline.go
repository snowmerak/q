package main

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
)

const Alphabet = "ZPMQVRWSNKTXJBYH"

// HashLine computes a 2-character anchor hash from content and line number.
func HashLine(content string, lineNum int) string {
	// Normalize CRLF to LF just in case
	content = strings.ReplaceAll(content, "\r", "")
	input := fmt.Sprintf("%d:%s", lineNum, content)

	h := fnv.New32a()
	_, _ = h.Write([]byte(input))
	hashVal := h.Sum32()

	idx1 := hashVal % 16
	idx2 := (hashVal / 16) % 16

	return string([]byte{Alphabet[idx1], Alphabet[idx2]})
}

// FormatReadOutput prefixes each line with "lineNum#hash:"
func FormatReadOutput(lines []string, startLine int) []string {
	formatted := make([]string, len(lines))
	for i, line := range lines {
		lineNum := startLine + i
		hash := HashLine(line, lineNum)
		formatted[i] = fmt.Sprintf("%d#%s:%s", lineNum, hash, line)
	}
	return formatted
}

type Anchor struct {
	Line int
	Hash string
}

var anchorRegex = regexp.MustCompile(`^(\d+)#([ZPMQVRWSNKTXJBYH]{2})$`)

// ParseAnchor parses "10#VR" into line number and hash.
func ParseAnchor(anchor string) (*Anchor, error) {
	matches := anchorRegex.FindStringSubmatch(anchor)
	if len(matches) != 3 {
		return nil, fmt.Errorf("invalid anchor format: %s", anchor)
	}
	line, _ := strconv.Atoi(matches[1])
	return &Anchor{Line: line, Hash: matches[2]}, nil
}

// GenerateDiffPreview creates a diff preview string for removed and added lines.
func GenerateDiffPreview(oldLines []string, newLines []string, startLine int, oldHashes []string, newHashes []string) string {
	var sb strings.Builder
	sb.WriteString("Diff preview:\n")
	for i, line := range oldLines {
		sb.WriteString(fmt.Sprintf("- %d#%s:%s\n", startLine+i, oldHashes[i], line))
	}
	for i, line := range newLines {
		sb.WriteString(fmt.Sprintf("+ %d#%s:%s\n", startLine+i, newHashes[i], line))
	}
	return strings.TrimSpace(sb.String())
}
