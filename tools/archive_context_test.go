package tools

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/tools/builtin"
)

func TestArchiveSearchScopeSurvivesMCPAndFiltersLoomSource(t *testing.T) {
	root := t.TempDir()
	archive, err := sessionstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = archive.Close() })
	for index, taskID := range []string{"", "child"} {
		message := client.Message{Role: client.RoleAssistant, Content: "archive response", ToolCalls: []client.ToolCall{{ID: "search"}}}
		var value any = message
		if taskID == "" {
			value = map[string]any{"message": message}
		}
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Save(sessionstore.Record{
			ID: fmt.Sprintf("message-%d", index), Kind: sessionstore.KindMessage, RunID: "run", TaskID: taskID,
			Content: message.TextContent(), Payload: payload, CreatedAt: time.Now().UTC().Add(-time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := NewRuntimeWithArchive(t.Context(), root, archive)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	call := client.ToolCall{ID: "search", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "search_archive", Arguments: `{"limit":1}`}}

	// Both agents intentionally reuse a provider call ID and share one runtime.
	// Each must see the other agent's response, including the captured artifact.
	var wait sync.WaitGroup
	for index, taskID := range []string{"", "child"} {
		wait.Go(func() {
			ctx := sessionstore.WithSearchScope(t.Context(), "run", taskID)
			result, err := runtime.Call(ctx, call)
			if err != nil || result.IsError {
				t.Errorf("search: %#v, error = %v", result, err)
				return
			}
			var receipt loomReceipt
			if err := json.Unmarshal([]byte(result.Content), &receipt); err != nil {
				t.Error(err)
				return
			}
			stored, err := runtime.loom.Store.ReadAll(ctx, receipt.LoomRef, 2<<20)
			if err != nil {
				t.Error(err)
				return
			}
			var captured struct {
				Structured builtin.SearchArchiveOutput `json:"structured"`
			}
			if err := json.Unmarshal(stored, &captured); err != nil {
				t.Error(err)
				return
			}
			output := captured.Structured
			if output.Total != 1 || len(output.Hits) != 1 || output.Hits[0].ID != fmt.Sprintf("message-%d", 1-index) {
				t.Errorf("scope %q stored output = %#v", taskID, output)
			}
		})
	}
	wait.Wait()
	result, err := runtime.Call(t.Context(), call)
	if err != nil || result.IsError {
		t.Fatalf("unscoped search: %#v, error = %v", result, err)
	}
	var output builtin.SearchArchiveOutput
	if err := decodeReceiptResult(result.Content, &output); err != nil {
		t.Fatal(err)
	}
	if output.Total != 2 {
		t.Fatalf("scope leaked to the next call: %#v", output)
	}
}
