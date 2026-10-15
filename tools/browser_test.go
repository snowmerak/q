package tools

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/loom"
)

func TestRawBrowserToolsAreDisabledWhileSearchRemainsAvailable(t *testing.T) {
	runtime, err := newRuntimeWithLSP(t.Context(), sameRuntimeRoots(t.TempDir()), nil, nil, loom.NewProcessEvaluator(), loom.StoreOptions{}, nil, nil, WithWebSearch(&fakeWebSearcher{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	listed, err := runtime.client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	for _, name := range []string{"browser_open", "browser_read", "browser_snapshot", "browser_click", "browser_fill", "browser_close"} {
		if runtimeHasTool(runtime, name) || names[name] {
			t.Fatalf("disabled browser tool exposed: %s", name)
		}
		result, err := runtime.Call(t.Context(), client.ToolCall{Function: client.FunctionCall{Name: name, Arguments: `{}`}})
		if err == nil && !result.IsError {
			t.Fatalf("disabled runtime tool accepted: %s", name)
		}
		resultMCP, err := runtime.client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err == nil && !resultMCP.IsError {
			t.Fatalf("disabled MCP tool accepted: %s", name)
		}
	}
	for _, name := range []string{"browser_search", "web_search", "web_search_batch"} {
		if !runtimeHasTool(runtime, name) || !names[name] {
			t.Fatalf("search tool missing: %s", name)
		}
	}
}
