package builtin

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const workspaceIgnoreFile = ".qignore"

type discoveryIgnore struct {
	rules []discoveryIgnoreRule
}

type discoveryIgnoreRule struct {
	negated       bool
	directoryOnly bool
	pattern       *regexp.Regexp
}

func loadDiscoveryIgnore(root string) (discoveryIgnore, error) {
	body, err := os.ReadFile(filepath.Join(root, workspaceIgnoreFile))
	if errors.Is(err, os.ErrNotExist) {
		return discoveryIgnore{}, nil
	}
	if err != nil {
		return discoveryIgnore{}, err
	}
	return parseDiscoveryIgnore(string(body)), nil
}

func parseDiscoveryIgnore(body string) discoveryIgnore {
	var result discoveryIgnore
	for _, raw := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negated := strings.HasPrefix(line, "!")
		if negated {
			line = strings.TrimSpace(strings.TrimPrefix(line, "!"))
		}
		if line == "" {
			continue
		}
		directoryOnly := strings.HasSuffix(line, "/") || strings.HasSuffix(line, `\`)
		line = strings.TrimRight(line, `/\`)
		anchored := strings.HasPrefix(line, "/") || strings.HasPrefix(line, `\`)
		line = strings.TrimLeft(line, `/\`)
		line = filepath.ToSlash(line)
		if line == "" {
			continue
		}
		expression := discoveryGlobExpression(line)
		if !anchored && !strings.Contains(line, "/") {
			expression = `(?:^|.*/)` + expression
		} else {
			expression = `^` + expression
		}
		result.rules = append(result.rules, discoveryIgnoreRule{
			negated: negated, directoryOnly: directoryOnly,
			pattern: regexp.MustCompile(expression + `$`),
		})
	}
	return result
}

func discoveryGlobExpression(pattern string) string {
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

func (ignore discoveryIgnore) matches(path string, directory bool) bool {
	path = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
	if path == ".q" && directory {
		return true
	}
	ignored := false
	for _, rule := range ignore.rules {
		if rule.directoryOnly && !directory {
			continue
		}
		if rule.pattern.MatchString(path) {
			ignored = !rule.negated
		}
	}
	return ignored
}
