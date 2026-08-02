package builtin

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/OneOfOne/xxhash"
)

const alphabet = "ZPMQVRWSNKTXJBYH"

// HashLine returns the two-character XXH32 anchor for content at a 1-based
// line number. The format intentionally matches the supplied reference.
func HashLine(content string, lineNum int) string {
	input := fmt.Sprintf("%d:%s", lineNum, content)
	h := xxhash.ChecksumString32S(input, 0)
	return string([]byte{alphabet[h%16], alphabet[(h/16)%16]})
}

// FormatReadOutput formats lines as "N#HH:content".
func FormatReadOutput(lines []string, startLine int) string {
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		n := startLine + i
		fmt.Fprintf(&b, "%d#%s:%s", n, HashLine(line, n), line)
	}
	return b.String()
}

// Anchor is a parsed LINE#HASH token.
type Anchor struct {
	Line int
	Hash string
}

var anchorRE = regexp.MustCompile(`^(\d+)#([ZPMQVRWSNKTXJBYH]{2})$`)

// ParseAnchor parses an exact LINE#HASH token.
func ParseAnchor(value string) (Anchor, bool) {
	match := anchorRE.FindStringSubmatch(value)
	if match == nil {
		return Anchor{}, false
	}
	line, err := strconv.Atoi(match[1])
	if err != nil || line < 1 {
		return Anchor{}, false
	}
	return Anchor{Line: line, Hash: match[2]}, true
}

func generateDiffPreview(oldLines, newLines []string, startLine int) string {
	var b strings.Builder
	b.WriteString("Diff preview:\n")
	for i, line := range oldLines {
		fmt.Fprintf(&b, "- %d#%s:%s\n", startLine+i, HashLine(line, startLine+i), line)
	}
	for i, line := range newLines {
		fmt.Fprintf(&b, "+ %d#%s:%s\n", startLine+i, HashLine(line, startLine+i), line)
	}
	return strings.TrimRight(b.String(), "\n")
}
