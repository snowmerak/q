package workspace

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/snowmerak/q/lsp"
)

var lspDiscoverySkippedDirectories = map[string]struct{}{
	".git": {}, ".hg": {}, ".svn": {}, ".q": {}, "node_modules": {},
	"vendor": {}, "target": {}, "dist": {}, "build": {}, ".venv": {}, "venv": {},
}

type lspMarker struct {
	path      string
	language  string
	dominates bool
}

// DiscoverLSPRoots finds common language project markers without changing the
// workspace. Nested module markers are folded under language-level workspace
// markers such as go.work and Cargo.toml files containing [workspace].
func (s Store) DiscoverLSPRoots() ([]lsp.RootConfig, error) {
	return s.DiscoverLSPRootsContext(context.Background())
}

func (s Store) DiscoverLSPRootsContext(ctx context.Context) ([]lsp.RootConfig, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ignore, err := loadLSPIgnore(s.Root)
	if err != nil {
		return nil, err
	}
	var markers []lspMarker
	err = filepath.WalkDir(s.Root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(s.Root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.IsDir() {
			if _, skip := lspDiscoverySkippedDirectories[strings.ToLower(entry.Name())]; skip || ignore.matches(relative, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if ignore.matches(relative, false) {
			return nil
		}
		directory := filepath.Dir(relative)
		if directory == "" {
			directory = "."
		}
		switch strings.ToLower(entry.Name()) {
		case "go.work":
			markers = append(markers, lspMarker{directory, "go", true})
		case "go.mod":
			markers = append(markers, lspMarker{directory, "go", false})
		case "cargo.toml":
			body, readErr := os.ReadFile(path)
			dominates := readErr == nil && regexp.MustCompile(`(?m)^\s*\[workspace\]\s*$`).Match(body)
			markers = append(markers, lspMarker{directory, "rust", dominates})
		case "tsconfig.json":
			markers = append(markers, lspMarker{directory, "typescript", false})
		case "jsconfig.json":
			markers = append(markers, lspMarker{directory, "javascript", false})
		case "pyproject.toml":
			markers = append(markers, lspMarker{directory, "python", false})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	result := make([]lsp.RootConfig, 0, len(markers))
	for _, marker := range markers {
		if !marker.dominates && dominatedByMarker(marker, markers) {
			continue
		}
		normalized, err := lsp.NormalizeRoot(lsp.RootConfig{
			Path: marker.path, Language: marker.language, Source: lsp.RootSourceDiscovered,
		})
		if err != nil {
			continue
		}
		key := strings.ToLower(normalized.Path) + "\x00" + normalized.Language
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, normalized)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path == result[j].Path {
			return result[i].Language < result[j].Language
		}
		return result[i].Path < result[j].Path
	})
	return result, nil
}

func dominatedByMarker(candidate lspMarker, markers []lspMarker) bool {
	for _, marker := range markers {
		if !marker.dominates || marker.language != candidate.language || marker.path == candidate.path {
			continue
		}
		relative, err := filepath.Rel(marker.path, candidate.path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

type lspIgnore struct{ rules []lspIgnoreRule }
type lspIgnoreRule struct {
	negated, directoryOnly bool
	pattern                *regexp.Regexp
}

func loadLSPIgnore(root string) (lspIgnore, error) {
	body, err := os.ReadFile(filepath.Join(root, IgnoreFileName))
	if errors.Is(err, os.ErrNotExist) {
		return lspIgnore{}, nil
	}
	if err != nil {
		return lspIgnore{}, err
	}
	var result lspIgnore
	for _, raw := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
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
		expression := lspGlobExpression(line)
		if !anchored && !strings.Contains(line, "/") {
			expression = `(?:^|.*/)` + expression
		} else {
			expression = `^` + expression
		}
		result.rules = append(result.rules, lspIgnoreRule{negated, directoryOnly, regexp.MustCompile(expression + `$`)})
	}
	return result, nil
}

func lspGlobExpression(pattern string) string {
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

func (ignore lspIgnore) matches(path string, directory bool) bool {
	path = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
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
