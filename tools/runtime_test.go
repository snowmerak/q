package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/tools/builtin"
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

func TestRuntimeExposesAndCallsArchiveTools(t *testing.T) {
	root := t.TempDir()
	archive, err := sessionstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if _, err := archive.Save(sessionstore.Record{
		ID: "decision-1", Kind: sessionstore.KindMessage, Role: "planner",
		Status: sessionstore.StatusSucceeded, Summary: "approved storage design",
		Content: "Use the workspace archive for durable agent history.",
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntimeWithArchive(context.Background(), root, archive)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if len(runtime.Tools()) != 13 || !runtimeHasTool(runtime, "search_archive") || !runtimeHasTool(runtime, "get_archive_record") {
		t.Fatalf("runtime tools = %#v", runtime.Tools())
	}

	searched, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-search", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "search_archive", Arguments: `{"query":"durable agent history"}`},
	})
	if err != nil || searched.IsError {
		t.Fatalf("search result = %#v, err = %v", searched, err)
	}
	var searchOutput builtin.SearchArchiveOutput
	if err := json.Unmarshal([]byte(searched.Content), &searchOutput); err != nil {
		t.Fatal(err)
	}
	if len(searchOutput.Hits) != 1 || searchOutput.Hits[0].ID != "decision-1" {
		t.Fatalf("search output = %#v", searchOutput)
	}

	got, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-get", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "get_archive_record", Arguments: `{"id":"decision-1"}`},
	})
	if err != nil || got.IsError {
		t.Fatalf("get result = %#v, err = %v", got, err)
	}
	var record builtin.GetArchiveRecordOutput
	if err := json.Unmarshal([]byte(got.Content), &record); err != nil {
		t.Fatal(err)
	}
	if record.ID != "decision-1" || record.Content != "Use the workspace archive for durable agent history." {
		t.Fatalf("record = %#v", record)
	}
}

func runtimeHasTool(runtime *Runtime, name string) bool {
	for _, tool := range runtime.Tools() {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}
