package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/snowmerak/q/v2/internal/fsutil"
	"github.com/snowmerak/q/v2/pkg/domain"
)

const lineAnchorAlphabet = "ZPMQVRWSNKTXJBYH"

var lineAnchorRE = regexp.MustCompile(`^(\d+)#([ZPMQVRWSNKTXJBYH]{2})$`)

func lineHash(content string, lineNum int) string {
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "%d:%s", lineNum, content)
	sum := h.Sum32()
	return string([]byte{lineAnchorAlphabet[sum%16], lineAnchorAlphabet[(sum/16)%16]})
}

func formatLineAnchors(lines []string, startLine int) string {
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		n := startLine + i
		fmt.Fprintf(&b, "%d#%s:%s", n, lineHash(line, n), line)
	}
	return b.String()
}

type pathGuard struct {
	allowedRoots []string
	dataDir      string
}

func newPathGuard(allowedRoots []string, dataDir string) (*pathGuard, error) {
	roots := make([]string, 0, len(allowedRoots))
	for _, r := range allowedRoots {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		roots = append(roots, filepath.Clean(abs))
	}
	if len(roots) == 0 {
		return nil, errors.New("file access denied: tools.allowed_roots is empty")
	}
	var cleanDataDir string
	if strings.TrimSpace(dataDir) != "" {
		if abs, err := filepath.Abs(dataDir); err == nil {
			if resolved, err := filepath.EvalSymlinks(abs); err == nil {
				abs = resolved
			}
			cleanDataDir = filepath.Clean(abs)
		}
	}
	return &pathGuard{allowedRoots: roots, dataDir: cleanDataDir}, nil
}

// resolve validates that rawPath lies lexically AND via resolved symlinks inside
// allowed_roots, while rejecting protected Q data-dir internals.
func (g *pathGuard) resolve(rawPath string) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", errors.New("path is required")
	}
	candidate := rawPath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(g.allowedRoots[0], candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if !g.withinAllowed(abs) {
		return "", fmt.Errorf("path %q escapes configured allowed_roots", rawPath)
	}

	resolved, err := evalSymlinksWithMissingTail(abs)
	if err != nil {
		return "", err
	}
	if !g.withinAllowed(resolved) {
		return "", fmt.Errorf("symlink %q escapes configured allowed_roots", rawPath)
	}
	if g.isProtectedInternal(resolved) {
		return "", fmt.Errorf("access to protected q internal storage %q is denied", rawPath)
	}
	return resolved, nil
}

func (g *pathGuard) withinAllowed(target string) bool {
	for _, root := range g.allowedRoots {
		rel, err := filepath.Rel(root, target)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

func (g *pathGuard) isProtectedInternal(target string) bool {
	if g.dataDir == "" {
		return false
	}
	rel, err := filepath.Rel(g.dataDir, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	norm := filepath.ToSlash(rel)
	top, _, _ := strings.Cut(norm, "/")
	switch {
	case top == "index" || top == "blobs" || top == "instance.lock" || top == "config.json":
		return true
	case strings.HasPrefix(top, "q.sqlite"):
		return true
	}
	return false
}

func evalSymlinksWithMissingTail(abs string) (string, error) {
	cur := abs
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			out := resolved
			for i := len(tail) - 1; i >= 0; i-- {
				out = filepath.Join(out, tail[i])
			}
			return filepath.Clean(out), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return filepath.Clean(abs), nil
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

func registerFileTools(r *Registry) {
	guardFor := func(call CallContext) (*pathGuard, error) {
		return newPathGuard(call.AllowedRoots, call.DataDir)
	}

	r.register(ToolSpec{
		Name:        "read_file",
		Description: "Read UTF-8 text file lines formatted with LINE#HASH anchors for safe edit_file anchoring.",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskRead,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":       map[string]any{"type": "string"},
				"start_line": map[string]any{"type": "integer", "description": "1-based start line (default 1)"},
				"end_line":   map[string]any{"type": "integer", "description": "1-based inclusive end line"},
			},
			"required": []string{"path"},
		},
		Handler: func(_ context.Context, call CallContext, argsJSON string) (ToolOutput, error) {
			guard, err := guardFor(call)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			var args struct {
				Path      string `json:"path"`
				StartLine int    `json:"start_line"`
				EndLine   int    `json:"end_line"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			target, err := guard.resolve(args.Path)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			data, err := os.ReadFile(target)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
			start := max(1, args.StartLine)
			if start > len(lines) {
				return ToolOutput{Content: fmt.Sprintf("start_line %d exceeds file length %d", start, len(lines)), IsError: true}, nil
			}
			end := args.EndLine
			if end <= 0 || end > len(lines) {
				end = len(lines)
			}
			if end < start {
				return ToolOutput{Content: "end_line < start_line", IsError: true}, nil
			}
			return ToolOutput{Content: formatLineAnchors(lines[start-1:end], start)}, nil
		},
	})

	r.register(ToolSpec{
		Name:        "edit_file",
		Description: "Replace an anchored line range [start_anchor..end_anchor] using LINE#HASH tokens from read_file, written atomically.",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskWrite,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":         map[string]any{"type": "string"},
				"start_anchor": map[string]any{"type": "string", "description": "LINE#HASH anchor of first line to replace"},
				"end_anchor":   map[string]any{"type": "string", "description": "Optional LINE#HASH anchor of last line to replace (defaults to start_anchor)"},
				"new_content":  map[string]any{"type": "string"},
			},
			"required": []string{"path", "start_anchor", "new_content"},
		},
		Handler: func(_ context.Context, call CallContext, argsJSON string) (ToolOutput, error) {
			guard, err := guardFor(call)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			var args struct {
				Path        string `json:"path"`
				StartAnchor string `json:"start_anchor"`
				EndAnchor   string `json:"end_anchor"`
				NewContent  string `json:"new_content"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			if args.EndAnchor == "" {
				args.EndAnchor = args.StartAnchor
			}
			startLine, startHash, ok1 := parseAnchorToken(args.StartAnchor)
			endLine, endHash, ok2 := parseAnchorToken(args.EndAnchor)
			if !ok1 || !ok2 || endLine < startLine {
				return ToolOutput{Content: "invalid LINE#HASH anchor(s)", IsError: true}, nil
			}
			target, err := guard.resolve(args.Path)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			data, err := os.ReadFile(target)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
			if endLine > len(lines) {
				return ToolOutput{Content: fmt.Sprintf("anchor line %d out of range (%d lines)", endLine, len(lines)), IsError: true}, nil
			}
			if got := lineHash(lines[startLine-1], startLine); got != startHash {
				return ToolOutput{Content: fmt.Sprintf("stale start_anchor %s (current %d#%s); re-read file", args.StartAnchor, startLine, got), IsError: true}, nil
			}
			if got := lineHash(lines[endLine-1], endLine); got != endHash {
				return ToolOutput{Content: fmt.Sprintf("stale end_anchor %s (current %d#%s); re-read file", args.EndAnchor, endLine, got), IsError: true}, nil
			}

			replacementLines := strings.Split(strings.ReplaceAll(args.NewContent, "\r\n", "\n"), "\n")
			merged := make([]string, 0, len(lines)-(endLine-startLine+1)+len(replacementLines))
			merged = append(merged, lines[:startLine-1]...)
			merged = append(merged, replacementLines...)
			merged = append(merged, lines[endLine:]...)
			if err := fsutil.WriteFileAtomic(target, []byte(strings.Join(merged, "\n")), 0o644); err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			return ToolOutput{Content: fmt.Sprintf("Edited %s lines %d..%d (%d replacement lines).", args.Path, startLine, endLine, len(replacementLines))}, nil
		},
	})

	r.register(ToolSpec{
		Name:        "write_file",
		Description: "Atomically create or overwrite a file within allowed_roots.",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskWrite,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			"required": []string{"path", "content"},
		},
		Handler: func(_ context.Context, call CallContext, argsJSON string) (ToolOutput, error) {
			guard, err := guardFor(call)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			var args struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			target, err := guard.resolve(args.Path)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			if err := fsutil.WriteFileAtomic(target, []byte(args.Content), 0o644); err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			return ToolOutput{Content: fmt.Sprintf("Wrote %d bytes to %s.", len(args.Content), args.Path)}, nil
		},
	})

	r.register(ToolSpec{
		Name:        "list_directory",
		Description: "List entries inside a directory within allowed_roots.",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskRead,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required": []string{"path"},
		},
		Handler: func(_ context.Context, call CallContext, argsJSON string) (ToolOutput, error) {
			guard, err := guardFor(call)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			var args struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			target, err := guard.resolve(args.Path)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			entries, err := os.ReadDir(target)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
			var b strings.Builder
			for _, e := range entries {
				kind := "file"
				if e.IsDir() {
					kind = "dir"
				}
				fmt.Fprintf(&b, "%s\t%s\n", kind, e.Name())
			}
			return ToolOutput{Content: strings.TrimSpace(b.String())}, nil
		},
	})

	r.register(ToolSpec{
		Name:        "create_directory",
		Description: "Create a directory (and parents) inside allowed_roots.",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskWrite,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required": []string{"path"},
		},
		Handler: func(_ context.Context, call CallContext, argsJSON string) (ToolOutput, error) {
			guard, err := guardFor(call)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			var args struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			target, err := guard.resolve(args.Path)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			return ToolOutput{Content: "Created directory " + args.Path}, nil
		},
	})

	r.register(ToolSpec{
		Name:        "move_path",
		Description: "Move/rename a file or directory within allowed_roots.",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskWrite,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source":      map[string]any{"type": "string"},
				"destination": map[string]any{"type": "string"},
			},
			"required": []string{"source", "destination"},
		},
		Handler: func(_ context.Context, call CallContext, argsJSON string) (ToolOutput, error) {
			guard, err := guardFor(call)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			var args struct {
				Source      string `json:"source"`
				Destination string `json:"destination"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			src, err := guard.resolve(args.Source)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			dst, err := guard.resolve(args.Destination)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			if err := os.Rename(src, dst); err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			return ToolOutput{Content: fmt.Sprintf("Moved %s -> %s", args.Source, args.Destination)}, nil
		},
	})

	r.register(ToolSpec{
		Name:        "copy_path",
		Description: "Copy a file within allowed_roots.",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskWrite,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source":      map[string]any{"type": "string"},
				"destination": map[string]any{"type": "string"},
			},
			"required": []string{"source", "destination"},
		},
		Handler: func(_ context.Context, call CallContext, argsJSON string) (ToolOutput, error) {
			guard, err := guardFor(call)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			var args struct {
				Source      string `json:"source"`
				Destination string `json:"destination"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			src, err := guard.resolve(args.Source)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			dst, err := guard.resolve(args.Destination)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			inF, err := os.Open(src)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			defer inF.Close()
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			outF, err := os.Create(dst)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			_, copyErr := io.Copy(outF, inF)
			closeErr := outF.Close()
			if copyErr != nil {
				return ToolOutput{Content: copyErr.Error(), IsError: true}, nil
			}
			if closeErr != nil {
				return ToolOutput{Content: closeErr.Error(), IsError: true}, nil
			}
			return ToolOutput{Content: fmt.Sprintf("Copied %s -> %s", args.Source, args.Destination)}, nil
		},
	})

	r.register(ToolSpec{
		Name:        "remove_path",
		Description: "Remove a file or directory tree inside allowed_roots (cannot remove an allowed_root itself).",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskWrite,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":      map[string]any{"type": "string"},
				"recursive": map[string]any{"type": "boolean"},
			},
			"required": []string{"path"},
		},
		Handler: func(_ context.Context, call CallContext, argsJSON string) (ToolOutput, error) {
			guard, err := guardFor(call)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			var args struct {
				Path      string `json:"path"`
				Recursive bool   `json:"recursive"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			target, err := guard.resolve(args.Path)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			for _, root := range guard.allowedRoots {
				if target == root {
					return ToolOutput{Content: "refusing to remove allowed_root itself", IsError: true}, nil
				}
			}
			if args.Recursive {
				err = os.RemoveAll(target)
			} else {
				err = os.Remove(target)
			}
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			return ToolOutput{Content: "Removed " + args.Path}, nil
		},
	})
}

func parseAnchorToken(value string) (int, string, bool) {
	m := lineAnchorRE.FindStringSubmatch(strings.TrimSpace(value))
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return 0, "", false
	}
	return n, m[2], true
}
