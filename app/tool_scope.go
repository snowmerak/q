package app

import "github.com/snowmerak/q/agentloop"

// ScopeTools preserves the original app API and delegates role filtering to
// the standalone loop package.
func ScopeTools(base AgentToolRuntime, role string) AgentToolRuntime {
	return agentloop.ScopeTools(base, role)
}
