package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/loom"
)

func TestConcurrentBuiltinCallsCaptureDistinctLoomResults(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	const count = 8
	for index := range count {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%d.txt", index)), []byte(fmt.Sprintf("marker-%d", index)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := NewRuntime(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	results := make([]client.ToolResult, count)
	callErrors := make([]error, count)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for index := range count {
		workers.Go(func() {
			<-start
			results[index], callErrors[index] = runtime.Call(ctx, client.ToolCall{
				ID: fmt.Sprintf("read-%d", index), Type: client.ToolTypeFunction,
				Function: client.FunctionCall{Name: "read_file", Arguments: fmt.Sprintf(`{"path":"file-%d.txt"}`, index)},
			})
		})
	}
	close(start)
	workers.Wait()
	refs := make(map[loom.Ref]bool)
	for index, result := range results {
		if callErrors[index] != nil || result.IsError {
			t.Fatalf("call %d = %#v, %v", index, result, callErrors[index])
		}
		var receipt loomReceipt
		if err := json.Unmarshal([]byte(result.Content), &receipt); err != nil {
			t.Fatal(err)
		}
		if refs[receipt.LoomRef] {
			t.Fatalf("calls shared an artifact: %s", receipt.LoomRef)
		}
		refs[receipt.LoomRef] = true
		artifact, err := runtime.loom.Store.Inspect(ctx, receipt.LoomRef)
		if err != nil || artifact.Source["call_id"] != fmt.Sprintf("read-%d", index) {
			t.Fatalf("call %d source = %#v, %v", index, artifact.Source, err)
		}
		content, err := runtime.loom.Store.ReadAll(ctx, receipt.LoomRef, 64<<10)
		if err != nil || !strings.Contains(string(content), fmt.Sprintf("marker-%d", index)) {
			t.Fatalf("call %d artifact content = %s, %v", index, content, err)
		}
	}
}
