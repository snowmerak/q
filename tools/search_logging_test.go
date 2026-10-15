package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/diagnostics"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/websearch"
)

func TestWebSearchMCPPreservesLoggerWithoutBorrowedChatModel(t *testing.T) {
	var logs bytes.Buffer
	directory := t.TempDir()
	sink, err := diagnostics.Open(directory, diagnostics.Options{Level: slog.LevelInfo, Stdout: true, FileEnabled: true, MaxFileBytes: 1 << 20, MaxTotalBytes: 2 << 20, MaxAge: time.Hour}, &logs, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close(t.Context()) })
	ctx := diagnostics.WithRequestID(diagnostics.WithLogger(t.Context(), sink.Logger().With("session_id", "session", "run_id", "run")), "")
	service := batchSearchFunc(func(ctx context.Context, _ websearch.Input) (websearch.Output, error) {
		diagnostics.Logger(ctx).Info("web_search.test.stage")
		return websearch.Output{Status: "complete"}, nil
	})
	r, err := newRuntimeWithLSP(t.Context(), sameRuntimeRoots(t.TempDir()), nil, nil, loom.NewProcessEvaluator(), loom.StoreOptions{}, nil, nil, WithWebSearch(service))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	result, err := r.Call(ctx, client.ToolCall{ID: "search-call", Function: client.FunctionCall{Name: "browser_search", Arguments: `{"goal":"private test goal"}`}})
	if err != nil || result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := sink.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
		t.Fatal("missing/invalid inner search log", logs.String(), err)
	}
	if record["request_id"] != diagnostics.RequestID(ctx) || record["tool_call_id"] != "search-call" || record["session_id"] != "session" || record["run_id"] != "run" {
		t.Fatal("MCP lost logging context", record)
	}
	if len(r.callContexts.pending) != 0 {
		t.Fatal("caller context retained after completion")
	}
	files, err := filepath.Glob(filepath.Join(directory, "logs", "studio", "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatal("search diagnostics file missing", files, err)
	}
	body, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, logs.Bytes()) {
		t.Fatal("file and stdout diagnostics differ")
	}
}
