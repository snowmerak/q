package app_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/lsp"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/websearch"
)

type embeddedWebSearch struct{ calls, bindings atomic.Int32 }

func (s *embeddedWebSearch) Search(ctx context.Context, in websearch.Input) (websearch.Output, error) {
	s.calls.Add(1)
	if caller, ok := client.ToolModelFromContext(ctx); ok && caller.Model == "test-model" && caller.ReasoningEffort == "high" && caller.Extra["caller_policy"] == "preserved" {
		s.bindings.Add(1)
	}
	return websearch.Output{Status: "complete", Requirements: []string{in.Goal}, Evidence: []websearch.Evidence{{URL: "https://example.com/source", Text: "Public loop receives web evidence."}}}, nil
}

type webLoopClient struct {
	embeddedClient
	tool, arguments string
}

func (c *webLoopClient) Chat(ctx context.Context, r client.ChatRequest) (*client.ChatResponse, error) {
	response, err := c.embeddedClient.Chat(ctx, r)
	if err == nil && len(c.requests) == 1 {
		response.Choices[0].Message.ToolCalls[0].Function = client.FunctionCall{Name: c.tool, Arguments: c.arguments}
	}
	return response, err
}

func TestPublicAgentLoopDispatchesWebSearchThroughMCP(t *testing.T) {
	for _, tc := range []struct {
		tool, arguments string
		calls           int32
	}{
		{tool: "web_search", arguments: `{"goal":"Read source evidence"}`, calls: 1},
		{tool: "web_search_batch", arguments: `{"tasks":[{"id":"one","search":{"goal":"first evidence"}},{"id":"two","search":{"goal":"second evidence"}}],"concurrency":2}`, calls: 2},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			root := t.TempDir()
			search := &embeddedWebSearch{}
			runtime, err := qtools.NewRuntimeWithRoots(t.Context(), qtools.RuntimeRoots{WorkspaceStateRoot: root, CheckoutRoot: root}, nil, loom.StoreOptions{}, lsp.GlobalConfig{}, lsp.WorkspaceConfig{}, nil, qtools.WithWebSearch(search))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = runtime.Close() }()
			chat := &webLoopClient{tool: tc.tool, arguments: tc.arguments}
			events := make(chan app.AgentEvent)
			go app.RunAgentLoop(t.Context(), app.AgentLoopRequest{Client: chat, Tools: runtime, Model: "test-model", ReasoningEffort: "high", Extra: map[string]any{"caller_policy": "preserved"}, Messages: []client.Message{{Role: client.RoleUser, Content: "Read source evidence"}}, WorkingDirectory: root}, events)
			resultSeen := false
			toolSeen := false
			for event := range events {
				if err := event.Err(); err != nil {
					t.Fatal(err)
				}
				if message, ok := event.Message(); ok && message.Role == client.RoleTool && message.Name == tc.tool {
					toolSeen = strings.Contains(message.TextContent(), "Public loop receives web evidence.") && strings.Contains(message.TextContent(), "loom_ref")
				}
				if _, ok := event.Result(); ok {
					resultSeen = true
				}
			}
			if !resultSeen || !toolSeen || search.calls.Load() != tc.calls || search.bindings.Load() != tc.calls || len(chat.requests) != 2 {
				t.Fatalf("result=%v tool=%v calls=%d requests=%d", resultSeen, toolSeen, search.calls.Load(), len(chat.requests))
			}
			advertised := false
			for _, tool := range chat.requests[0].Tools {
				if tool.Function.Name == tc.tool {
					advertised = true
				}
			}
			if !advertised {
				t.Fatalf("public loop did not advertise %s", tc.tool)
			}
		})
	}
}
