package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
)

// RoleRuntime is a non-owning view of Runtime that exposes only builtin tools
// and external MCP tools assigned to one role. Closing the underlying Runtime
// remains the caller's responsibility.
type RoleRuntime struct {
	runtime *Runtime
	role    string
}

// ForRole returns a ToolRuntime-compatible view with role-based MCP discovery
// and dispatch enforcement. The returned view does not own Runtime.
func (r *Runtime) ForRole(role string) *RoleRuntime {
	return &RoleRuntime{runtime: r, role: strings.TrimSpace(role)}
}

// Tools returns the schemas visible to this role.
func (r *RoleRuntime) Tools() []client.Tool {
	if r == nil || r.runtime == nil {
		return nil
	}
	return r.runtime.ToolsForRole(r.role)
}

// Call dispatches a builtin or MCP call only when it is present in Tools.
func (r *RoleRuntime) Call(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
	for _, tool := range r.Tools() {
		if tool.Function.Name == call.Function.Name {
			return r.runtime.Call(ctx, call)
		}
	}
	return client.ToolResult{}, fmt.Errorf("tools: tool %q is not assigned to role %q", call.Function.Name, r.role)
}

// Environment returns the host environment of the underlying Runtime.
func (r *RoleRuntime) Environment() HostEnvironment {
	if r == nil || r.runtime == nil {
		return HostEnvironment{}
	}
	return r.runtime.Environment()
}

// SearchSkillHints delegates host-side skill discovery. Callers should first
// ensure search_skills and get_skill are present in Tools.
func (r *RoleRuntime) SearchSkillHints(ctx context.Context, query string, limit int) (SkillHintSearchResult, error) {
	if r == nil || r.runtime == nil {
		return SkillHintSearchResult{}, fmt.Errorf("tools: runtime is unavailable")
	}
	return r.runtime.SearchSkillHints(ctx, query, limit)
}
