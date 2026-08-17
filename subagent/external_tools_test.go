package subagent

import (
	"testing"

	"github.com/snowmerak/q/client"
)

func TestAssignedExternalMCPToolsPassRoleFilters(t *testing.T) {
	external := client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "mcp_docs__lookup"}}
	runtime := &fakeScoutTools{available: []client.Tool{external}}
	tests := []struct {
		name  string
		tools []client.Tool
	}{
		{name: "scout", tools: scoutTools(runtime.Tools())},
		{name: "griller", tools: grillerTools(runtime.Tools())},
		{name: "planner", tools: plannerTools(runtime)},
		{name: "planner review", tools: plannerReviewTools(runtime.Tools())},
		{name: "coder", tools: coderTools(runtime.Tools())},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !hasScoutTool(test.tools, external.Function.Name) {
				t.Fatalf("external MCP tool missing from %s tools: %#v", test.name, test.tools)
			}
		})
	}
}
