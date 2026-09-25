package agentloop

import (
	"context"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
	qtools "github.com/snowmerak/q/tools"
)

type roleToolCatalog interface {
	ToolsForRole(string) []client.Tool
}

type scopedToolRuntime struct {
	base ToolRuntime
	role string
}

// ScopeTools exposes only tools assigned to role when a runtime has a
// role-aware catalog. Calls to hidden tools are rejected.
func ScopeTools(base ToolRuntime, role string) ToolRuntime {
	if base == nil {
		return nil
	}
	return &scopedToolRuntime{base: base, role: strings.TrimSpace(role)}
}

func (r *scopedToolRuntime) Tools() []client.Tool {
	if catalog, ok := r.base.(roleToolCatalog); ok {
		return catalog.ToolsForRole(r.role)
	}
	return r.base.Tools()
}

func (r *scopedToolRuntime) Environment() qtools.HostEnvironment {
	return r.base.Environment()
}

func (r *scopedToolRuntime) Call(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
	for _, tool := range r.Tools() {
		if tool.Function.Name == call.Function.Name {
			return r.base.Call(ctx, call)
		}
	}
	return client.ToolResult{}, fmt.Errorf("tool %q is not assigned to role %q", call.Function.Name, r.role)
}

func (r *scopedToolRuntime) SearchSkillHints(ctx context.Context, query string, limit int) (qtools.SkillHintSearchResult, error) {
	if !toolAvailable(r, "search_skills") {
		return qtools.SkillHintSearchResult{}, fmt.Errorf("tool %q is not assigned to role %q", "search_skills", r.role)
	}
	searcher, ok := r.base.(skillHintSearcher)
	if !ok {
		return qtools.SkillHintSearchResult{}, fmt.Errorf("agent skills hint search is unavailable")
	}
	return searcher.SearchSkillHints(ctx, query, limit)
}
