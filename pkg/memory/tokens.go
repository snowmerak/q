package memory

import (
	"strings"
	"unicode/utf8"
)

// EstimateTokens provides a deterministic, tokenizer-free approximation of
// token count suitable for context budgeting across multilingual prose.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	asciiChars := 0
	nonASCIIRunes := 0
	for _, r := range text {
		if r < utf8.RuneSelf {
			asciiChars++
		} else {
			nonASCIIRunes++
		}
	}
	tokens := nonASCIIRunes + (asciiChars+3)/4
	if tokens < 1 {
		return 1
	}
	return tokens
}

// ExtractJSON strips optional markdown code fences around a JSON payload.
func ExtractJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return strings.Trim(trimmed, "` \t\r\n")
	}
	start := 1
	end := len(lines)
	if strings.HasPrefix(strings.TrimSpace(lines[end-1]), "```") {
		end--
	}
	return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
}

// TruncateRunes truncates s to at most maxRunes Unicode code points and
// reports whether truncation occurred.
func TruncateRunes(s string, maxRunes int) (string, bool) {
	if maxRunes <= 0 || s == "" {
		return s, false
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i], true
		}
		count++
	}
	return s, false
}
