package tools

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/websearch"
)

func TestCallerContextCancellationAndCleanup(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	search := batchSearchFunc(func(ctx context.Context, _ websearch.Input) (websearch.Output, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return websearch.Output{}, ctx.Err()
	})
	runtime, err := newRuntimeWithLSP(t.Context(), sameRuntimeRoots(t.TempDir()), nil, nil, loom.NewProcessEvaluator(), loom.StoreOptions{}, nil, nil, WithWebSearch(search))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx = client.WithToolModel(ctx, client.ToolModel{Client: webPlannerChatFunc(func(context.Context, client.ChatRequest) (*client.ChatResponse, error) {
		return nil, errors.New("unexpected generation")
	}), Model: "caller"})
	returned := make(chan struct{})
	go func() {
		_, _ = runtime.Call(ctx, client.ToolCall{Function: client.FunctionCall{Name: "web_search", Arguments: `{"goal":"wait for cancellation"}`}})
		close(returned)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("MCP search did not start")
	}
	cancel()
	for _, event := range []<-chan struct{}{returned, stopped} {
		select {
		case <-event:
		case <-time.After(5 * time.Second):
			t.Fatal("caller cancellation did not stop MCP search")
		}
	}
	runtime.callContexts.mu.Lock()
	defer runtime.callContexts.mu.Unlock()
	if len(runtime.callContexts.pending) != 0 {
		t.Fatal("cancelled call retained caller client")
	}
}

func TestExpiredCallerContextCannotBeReused(t *testing.T) {
	contexts := &toolCallContexts{}
	ctx := client.WithToolModel(t.Context(), client.ToolModel{Client: webPlannerChatFunc(func(context.Context, client.ChatRequest) (*client.ChatResponse, error) {
		return nil, errors.New("unexpected generation")
	}), Model: "caller"})
	params := &mcp.CallToolParams{Name: "web_search"}
	release := contexts.bind(ctx, params)
	release()
	called := false
	handler := contexts.middleware(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		called = true
		return nil, nil
	})
	_, err := handler(t.Context(), "tools/call", &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "web_search", Meta: params.Meta}})
	if err == nil || called || errors.Is(err, context.Canceled) {
		t.Fatalf("expired context admitted: called=%v error=%v", called, err)
	}
}
