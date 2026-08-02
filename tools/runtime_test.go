package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestRuntimeListsAndCallsBuiltinTools(t *testing.T) {
	root := t.TempDir()
	runtime, err := NewRuntime(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if len(runtime.Tools()) != 11 {
		t.Fatalf("runtime tools = %d", len(runtime.Tools()))
	}
	result, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-1", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{
			Name: "write_file", Arguments: `{"path":"created.txt","content":"from tool"}`,
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("tool result = %#v, err = %v", result, err)
	}
	body, err := os.ReadFile(filepath.Join(root, "created.txt"))
	if err != nil || string(body) != "from tool" {
		t.Fatalf("created file = %q, err = %v", body, err)
	}
}
