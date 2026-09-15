package app

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

type scopedAgentToolRuntime struct {
	base agentToolRuntime
	role string
}

func scopeTools(base agentToolRuntime, role string) agentToolRuntime {
	if base == nil {
		return nil
	}
	return &scopedAgentToolRuntime{base: base, role: strings.TrimSpace(role)}
}

func (r *scopedAgentToolRuntime) Tools() []client.Tool {
	if catalog, ok := r.base.(roleToolCatalog); ok {
		return catalog.ToolsForRole(r.role)
	}
	return r.base.Tools()
}

func (r *scopedAgentToolRuntime) Environment() qtools.HostEnvironment {
	return r.base.Environment()
}

func (r *scopedAgentToolRuntime) Call(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
	for _, tool := range r.Tools() {
		if tool.Function.Name == call.Function.Name {
			return r.base.Call(ctx, call)
		}
	}
	return client.ToolResult{}, fmt.Errorf("tool %q is not assigned to role %q", call.Function.Name, r.role)
}

func (r *scopedAgentToolRuntime) SearchSkillHints(ctx context.Context, query string, limit int) (qtools.SkillHintSearchResult, error) {
	if !toolAvailable(r, "search_skills") {
		return qtools.SkillHintSearchResult{}, fmt.Errorf("tool %q is not assigned to role %q", "search_skills", r.role)
	}
	searcher, ok := r.base.(skillHintSearcher)
	if !ok {
		return qtools.SkillHintSearchResult{}, fmt.Errorf("Agent Skills hint search is unavailable")
	}
	return searcher.SearchSkillHints(ctx, query, limit)
}
