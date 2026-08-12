package subagent

import (
	"testing"

	"github.com/snowmerak/q/client"
)

func TestReadOnlyLSPToolsAreAvailableToQueryRoles(t *testing.T) {
	available := []client.Tool{
		scoutFunctionTool("lsp_definition"),
		scoutFunctionTool("lsp_workspace_symbols"),
		scoutFunctionTool("lsp_rename"),
	}
	for label, tools := range map[string][]client.Tool{
		"scout":          scoutTools(available),
		"planner review": plannerReviewTools(available),
		"coder":          coderTools(available),
	} {
		if !hasScoutTool(tools, "lsp_definition") || !hasScoutTool(tools, "lsp_workspace_symbols") {
			t.Fatalf("%s LSP tools = %#v", label, tools)
		}
		if label != "coder" && hasScoutTool(tools, "lsp_rename") {
			t.Fatalf("%s exposed an unapproved LSP mutation tool: %#v", label, tools)
		}
	}
}
