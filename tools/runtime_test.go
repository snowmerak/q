package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/tools/builtin"
	"github.com/snowmerak/q/workspace"
)

func TestRuntimeListsAndCallsBuiltinTools(t *testing.T) {
	root := t.TempDir()
	runtime, err := NewRuntime(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if len(runtime.Tools()) != 14 {
		t.Fatalf("runtime tools = %d", len(runtime.Tools()))
	}
	environment := runtime.Environment()
	if environment.OS != goruntime.GOOS || environment.Architecture != goruntime.GOARCH || environment.Shell == "" {
		t.Fatalf("runtime environment = %#v", environment)
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
	var receipt loomReceipt
	if err := json.Unmarshal([]byte(result.Content), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.LoomRef == "" || !receipt.Stored {
		t.Fatalf("Loom receipt = %#v", receipt)
	}
	artifact, err := runtime.loom.Store.Inspect(context.Background(), receipt.LoomRef)
	if err != nil || artifact.Source["tool"] != "write_file" {
		t.Fatalf("captured artifact = %#v, err = %v", artifact, err)
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
	if len(runtime.Tools()) != 16 || !runtimeHasTool(runtime, "search_archive") || !runtimeHasTool(runtime, "get_archive_record") {
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
	if err := decodeReceiptResult(searched.Content, &searchOutput); err != nil {
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
	if err := decodeReceiptResult(got.Content, &record); err != nil {
		t.Fatal(err)
	}
	if record.ID != "decision-1" || record.Content != "Use the workspace archive for durable agent history." {
		t.Fatalf("record = %#v", record)
	}
}

func TestRuntimeRootsComeFromCurrentSessionNotArchive(t *testing.T) {
	root := t.TempDir()
	sessionRef := loom.Ref("loom://0123456789abcdef0123456789abcdef")
	archiveRef := "loom://fedcba9876543210fedcba9876543210"
	if err := (workspace.Store{Root: root}).Save(workspace.Session{
		Transcript: []client.Message{{Role: client.RoleTool, Content: sessionRef.String()}},
	}); err != nil {
		t.Fatal(err)
	}
	archive, err := sessionstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if _, err := archive.Save(sessionstore.Record{
		Kind: sessionstore.KindResult, Refs: []string{archiveRef}, Content: archiveRef,
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := newRuntime(context.Background(), root, archive, loom.InProcessEvaluator{}, loom.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	refs, err := runtime.loom.Store.Options().Roots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != sessionRef {
		t.Fatalf("Loom roots = %#v", refs)
	}
}
func TestRuntimeEvaluatesCapturedMCPResult(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "one.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	var evaluator loom.Evaluator = loom.InProcessEvaluator{}
	if executable := os.Getenv("Q_LOOM_WORKER_EXECUTABLE"); executable != "" {
		evaluator = loom.ProcessEvaluator{Executable: executable}
	}
	runtime, err := newRuntime(context.Background(), root, nil, evaluator, loom.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	listed, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-list", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "list_directory", Arguments: `{}`},
	})
	if err != nil || listed.IsError {
		t.Fatalf("list result = %#v, err = %v", listed, err)
	}
	var captured loomReceipt
	if err := json.Unmarshal([]byte(listed.Content), &captured); err != nil {
		t.Fatal(err)
	}
	arguments, err := json.Marshal(map[string]any{
		"inputs": map[string]string{"source": captured.LoomRef.String()},
		"code":   `const entries = loom.json(inputs.source, "/structured/entries"); return {count: entries.filter(entry => entry.name === "one.txt").length};`,
	})
	if err != nil {
		t.Fatal(err)
	}
	evaluated, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "call-eval", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "loom_eval", Arguments: string(arguments)},
	})
	if err != nil || evaluated.IsError {
		t.Fatalf("eval result = %#v, err = %v", evaluated, err)
	}
	var output builtin.LoomEvalOutput
	if err := json.Unmarshal([]byte(evaluated.Content), &output); err != nil {
		t.Fatal(err)
	}
	value, ok := output.Value.(map[string]any)
	if !ok || value["count"] != float64(1) || len(output.Artifact.Parents) != 1 || output.Artifact.Parents[0] != captured.LoomRef {
		t.Fatalf("eval output = %#v", output)
	}
}

func TestTargetResolverUsesRealLoomRuntime(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{"one.go": "package sample", "two.txt": "two"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := newRuntime(context.Background(), root, nil, loom.InProcessEvaluator{}, loom.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	listed, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "target-list", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "list_directory", Arguments: `{}`},
	})
	if err != nil || listed.IsError {
		t.Fatalf("list result = %#v, err = %v", listed, err)
	}
	var captured loomReceipt
	if err := json.Unmarshal([]byte(listed.Content), &captured); err != nil {
		t.Fatal(err)
	}
	target := subagent.TargetCondition{Any: []subagent.TargetProduct{
		{All: []subagent.TargetSelector{
			{Kind: subagent.TargetSelectorPaths, Paths: []string{"one.go", "two.txt"}},
			{Kind: subagent.TargetSelectorLoom,
				Code:   `const entries = loom.json(inputs.tree, "/structured/entries"); return entries.filter(entry => entry.name.endsWith(".go")).map(entry => entry.name);`,
				Inputs: map[string]string{"tree": captured.LoomRef.String()}},
		}},
		{All: []subagent.TargetSelector{{Kind: subagent.TargetSelectorPaths, Paths: []string{"README.md"}}}},
	}}
	resolved, err := (subagent.TargetResolver{Tools: runtime}).Resolve(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"README.md", "one.go"}
	if !reflect.DeepEqual(resolved, want) {
		t.Fatalf("resolved targets = %#v, want %#v", resolved, want)
	}
}
func TestLargeToolResultReceiptKeepsOnlyBoundedPreview(t *testing.T) {
	artifact := loom.Artifact{
		Ref: "loom://0123456789abcdef0123456789abcdef", Kind: "mcp-result",
		Bytes: 1 << 20, Digest: strings.Repeat("a", 64),
	}
	encoded, err := encodeLoomReceipt(artifact, strings.Repeat("x", maximumInlineToolResult+1))
	if err != nil {
		t.Fatal(err)
	}
	var receipt loomReceipt
	if err := json.Unmarshal([]byte(encoded), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Result != nil || len([]rune(receipt.Preview)) != 4096 || receipt.LoomRef != artifact.Ref {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestCaptureMCPToolResultSupportsCustomServer(t *testing.T) {
	store, err := loom.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	call := client.ToolCall{
		ID: "custom-call-1", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "search", Arguments: `{"query":"loom"}`},
	}
	raw := &mcp.CallToolResult{StructuredContent: map[string]any{
		"matches": []any{"first", "second"},
	}}
	result, err := CaptureMCPToolResult(context.Background(), store, "custom-search", call, raw)
	if err != nil || result.IsError {
		t.Fatalf("captured custom MCP result = %#v, err = %v", result, err)
	}
	var receipt loomReceipt
	if err := json.Unmarshal([]byte(result.Content), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.LoomRef == "" || !receipt.Stored {
		t.Fatalf("Loom receipt = %#v", receipt)
	}
	artifact, err := store.Inspect(context.Background(), receipt.LoomRef)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Source["server"] != "custom-search" || artifact.Source["tool"] != "search" || artifact.Source["call_id"] != "custom-call-1" {
		t.Fatalf("captured artifact source = %#v", artifact.Source)
	}
}

func decodeReceiptResult(content string, output any) error {
	var receipt struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(content), &receipt); err != nil {
		return err
	}
	return json.Unmarshal(receipt.Result, output)
}

func runtimeHasTool(runtime *Runtime, name string) bool {
	for _, tool := range runtime.Tools() {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}
