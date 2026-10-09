// Package qignore implements the shared .qignore pattern syntax.
package qignore

import (
	"path/filepath"
	"regexp"
	"strings"
)

type rule struct {
	negated, directoryOnly bool
	pattern                *regexp.Regexp
}

// Matcher applies patterns in order; the last matching rule wins.
// Traversal defaults, including always-excluded directories, belong to callers.
type Matcher struct{ rules []rule }

func Parse(body string) Matcher {
	var result Matcher
	for raw := range strings.SplitSeq(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negated := strings.HasPrefix(line, "!")
		if negated {
			line = strings.TrimSpace(strings.TrimPrefix(line, "!"))
		}
		directoryOnly := strings.HasSuffix(line, "/") || strings.HasSuffix(line, `\`)
		line = strings.TrimRight(line, `/\`)
		anchored := strings.HasPrefix(line, "/") || strings.HasPrefix(line, `\`)
		line = filepath.ToSlash(strings.TrimLeft(line, `/\`))
		if line == "" {
			continue
		}
		expression := globExpression(line)
		if !anchored && !strings.Contains(line, "/") {
			expression = `(?:^|.*/)` + expression
		} else {
			expression = `^` + expression
		}
		result.rules = append(result.rules, rule{negated, directoryOnly, regexp.MustCompile(expression + `$`)})
	}
	return result
}

func globExpression(pattern string) string {
	var result strings.Builder
	for index := 0; index < len(pattern); index++ {
		switch pattern[index] {
		case '*':
			if index+1 < len(pattern) && pattern[index+1] == '*' {
				index++
				if index+1 < len(pattern) && pattern[index+1] == '/' {
					index++
					result.WriteString(`(?:.*/)?`)
				} else {
					result.WriteString(`.*`)
				}
			} else {
				result.WriteString(`[^/]*`)
			}
		case '?':
			result.WriteString(`[^/]`)
		default:
			result.WriteString(regexp.QuoteMeta(string(pattern[index])))
		}
	}
	return result.String()
}

func (m Matcher) Matches(path string, directory bool) bool {
	path = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
	ignored := false
	for _, rule := range m.rules {
		if rule.directoryOnly && !directory {
			continue
		}
		if rule.pattern.MatchString(path) {
			ignored = !rule.negated
		}
	}
	return ignored
}
