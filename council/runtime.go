package council

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/tools/builtin"
)

var councilReadTools = map[string]bool{
	"read_file": true, "list_directory": true, "loom_inspect": true, "loom_read": true,
}

// ReadRuntime is an authorization boundary: it hides and rejects every
// mutating builtin or external MCP operation, including run_command.
type ReadRuntime struct {
	base  agentloop.ToolRuntime
	roots []string
}

func NewReadRuntime(base agentloop.ToolRuntime, roots []string) *ReadRuntime {
	return &ReadRuntime{base: base, roots: append([]string(nil), roots...)}
}

func (r *ReadRuntime) Tools() []client.Tool {
	var result []client.Tool
	if len(r.roots) > 0 {
		result = append(result, client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
			Name: "search_text", Description: "Search text in the authorized workspace roots. Returns at most 100 matching lines, honors .qignore, and skips symlinks and large files.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}, "additionalProperties": false},
		}})
	}
	if r.base == nil {
		return result
	}
	for _, tool := range r.base.Tools() {
		if councilReadTools[tool.Function.Name] {
			result = append(result, tool)
		}
	}
	return result
}

func (r *ReadRuntime) Environment() tools.HostEnvironment {
	if r.base == nil {
		return tools.HostEnvironment{}
	}
	return r.base.Environment()
}

func (r *ReadRuntime) Call(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
	if call.Function.Name == "search_text" {
		var input struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
			return client.ToolResult{}, err
		}
		result, err := r.search(ctx, input.Query)
		if err != nil {
			return client.ToolResult{}, err
		}
		return client.ToolResult{Content: result}, nil
	}
	if !councilReadTools[call.Function.Name] {
		return client.ToolResult{}, fmt.Errorf("council tool %q is not read-only", call.Function.Name)
	}
	if r.base == nil {
		return client.ToolResult{}, errors.New("council tool runtime is unavailable")
	}
	for _, tool := range r.base.Tools() {
		if tool.Function.Name == call.Function.Name {
			return r.base.Call(ctx, call)
		}
	}
	return client.ToolResult{}, fmt.Errorf("council tool %q is unavailable", call.Function.Name)
}

func (r *ReadRuntime) search(ctx context.Context, query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 256 {
		return "", errors.New("search query must contain 1 to 256 characters")
	}
	if len(r.roots) == 0 {
		return "", errors.New("council has no workspace roots")
	}
	fs, err := builtin.NewFSWithRoots(r.roots[0], r.roots[1:])
	if err != nil {
		return "", err
	}
	defer fs.Close()
	needle := strings.ToLower(query)
	var result strings.Builder
	hits, files, directories := 0, 0, 0
	for _, root := range r.roots {
		guard, err := os.OpenRoot(root)
		if err != nil {
			return "", err
		}
		var walk func(string) error
		walk = func(directory string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if files >= 10000 || hits >= 100 || directories >= 10000 {
				return nil
			}
			directories++
			listing, err := fs.ListDirectory(builtin.ListDirectoryInput{Path: directory})
			if err != nil {
				return err
			}
			for _, entry := range listing.Entries {
				if entry.Name == ".git" || entry.Name == "node_modules" {
					continue
				}
				path := filepath.Join(directory, entry.Name)
				switch entry.Type {
				case "directory":
					if err := walk(path); err != nil {
						return err
					}
				case "file":
					if files >= 10000 || hits >= 100 {
						return nil
					}
					files++
					if entry.Size > 1<<20 {
						continue
					}
					relative, err := filepath.Rel(root, path)
					if err != nil {
						continue
					}
					file, err := guard.Open(relative)
					if err != nil {
						continue
					}
					scanner := bufio.NewScanner(file)
					scanner.Buffer(make([]byte, 64<<10), 1<<20)
					line := 0
					for scanner.Scan() {
						line++
						if strings.Contains(strings.ToLower(scanner.Text()), needle) {
							text := scanner.Text()
							if len(text) > 240 {
								text = text[:240]
							}
							fmt.Fprintf(&result, "%s:%d: %s\n", path, line, text)
							hits++
							if hits >= 100 {
								break
							}
						}
					}
					_ = file.Close()
				}
			}
			return nil
		}
		walkErr := walk(root)
		_ = guard.Close()
		if walkErr != nil {
			return "", walkErr
		}
		if hits >= 100 || files >= 10000 {
			break
		}
	}
	if hits == 0 {
		return "No matches.", nil
	}
	return result.String(), nil
}
