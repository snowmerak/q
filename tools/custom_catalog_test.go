package tools

import (
	"github.com/snowmerak/q/client"
	"testing"
)

func TestCustomCatalogIncludesConnectedServersOutsideDefaultRole(t *testing.T) {
	tool := func(name string) client.Tool { return client.Tool{Function: client.FunctionDefinition{Name: name}} }
	base := &Runtime{tools: []client.Tool{tool("read_file")}, external: map[string]*externalServer{"docs": {tools: []client.Tool{tool("mcp_docs__read")}}}}
	if len(base.ToolsForRole("default")) != 1 {
		t.Fatal("fixture unexpectedly assigns default MCP tools")
	}
	if len(base.CustomTools()) != 2 {
		t.Fatal("custom catalog omitted connected server")
	}
	scope := &ExternalScope{base: base, external: map[string]*externalServer{"session": {tools: []client.Tool{tool("mcp_session__read")}}}}
	if len(scope.CustomTools()) != 3 {
		t.Fatal("session MCP tools omitted")
	}
}
