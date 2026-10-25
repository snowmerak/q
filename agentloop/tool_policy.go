package agentloop

import (
	"context"
	"fmt"
	"github.com/snowmerak/q/client"
	qtools "github.com/snowmerak/q/tools"
)

// filterTools applies an optional host policy. Nil preserves the standard catalog;
// an explicitly empty list allows no tools, including loop-owned tools.
func filterTools(tools []client.Tool, names []string) []client.Tool {
	if names == nil {
		return tools
	}
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	result := make([]client.Tool, 0, len(tools))
	for _, tool := range tools {
		if allowed[tool.Function.Name] {
			result = append(result, tool)
		}
	}
	return result
}

// Keep optional host-side skill discovery subject to the same catalog policy.
type allowedToolRuntime struct {
	base  ToolRuntime
	names []string
}

func (r *allowedToolRuntime) Tools() []client.Tool                { return filterTools(r.base.Tools(), r.names) }
func (r *allowedToolRuntime) Environment() qtools.HostEnvironment { return r.base.Environment() }
func (r *allowedToolRuntime) Call(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
	if !catalogHas(r.Tools(), call.Function.Name) {
		return client.ToolResult{}, fmt.Errorf("tool %q is not allowed in this session", call.Function.Name)
	}
	return r.base.Call(ctx, call)
}
func (r *allowedToolRuntime) SearchSkillHints(ctx context.Context, query string, limit int) (qtools.SkillHintSearchResult, error) {
	if !catalogHas(r.Tools(), "search_skills") {
		return qtools.SkillHintSearchResult{}, nil
	}
	if searcher, ok := r.base.(skillHintSearcher); ok {
		return searcher.SearchSkillHints(ctx, query, limit)
	}
	return qtools.SkillHintSearchResult{}, nil
}

func catalogHas(tools []client.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}
