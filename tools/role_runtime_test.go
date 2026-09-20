package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestRoleRuntimeExposesCatalogAndRejectsUnassignedCalls(t *testing.T) {
	runtime := &Runtime{tools: []client.Tool{{
		Type:     client.ToolTypeFunction,
		Function: client.FunctionDefinition{Name: "allowed"},
	}}}
	view := runtime.ForRole(" default ")
	if tools := view.Tools(); len(tools) != 1 || tools[0].Function.Name != "allowed" {
		t.Fatalf("role tools = %#v", tools)
	}
	_, err := view.Call(context.Background(), client.ToolCall{
		Function: client.FunctionCall{Name: "not_assigned"},
	})
	if err == nil || !strings.Contains(err.Error(), `not_assigned`) || !strings.Contains(err.Error(), `default`) {
		t.Fatalf("unassigned call error = %v", err)
	}
}
