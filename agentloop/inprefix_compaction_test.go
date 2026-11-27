package agentloop_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
)

type funcChatClient func(context.Context, client.ChatRequest) (*client.ChatResponse, error)

func (f funcChatClient) Chat(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	return f(ctx, request)
}

func isInPrefixCheckpointRequest(request client.ChatRequest) bool {
	if len(request.Messages) == 0 {
		return false
	}
	last := request.Messages[len(request.Messages)-1]
	return last.Role == client.RoleUser && strings.Contains(last.Content, "session continuation checkpoint")
}

func isDetachedCheckpointRequest(request client.ChatRequest) bool {
	return len(request.Tools) == 0 && request.ConversationID == "" && len(request.Messages) > 0 &&
		strings.Contains(request.Messages[0].Content, "session continuation checkpoint")
}

func TestRunAgentLoopCompactsInPrefixAndResetsConversationAfterwards(t *testing.T) {
	var requests []client.ChatRequest
	chatClient := funcChatClient(func(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		request.Messages = append([]client.Message(nil), request.Messages...)
		request.Tools = append([]client.Tool(nil), request.Tools...)
		requests = append(requests, request)

		if isInPrefixCheckpointRequest(request) {
			body, _ := json.Marshal(memory.Checkpoint{ActiveWork: []string{"in-prefix checkpoint"}})
			return finalResponse(string(body)), nil
		}
		if len(requests) == 1 {
			resp := toolResponse("inspect")
			resp.ConversationID = "thread-before-compact"
			// Calibrate promptUsage high enough that appending the assistant
			// response triggers CompactBeforeAppend while tool history is complete.
			resp.Usage = client.Usage{PromptTokens: 13_580}
			return resp, nil
		}
		return finalResponse("done"), nil
	})
	runtime := &scriptedTools{name: "inspect", content: "ok"}
	events := make(chan agentloop.Event)
	go agentloop.RunAgentLoop(t.Context(), agentloop.Request{
		Client: chatClient, Tools: runtime, Model: "test",
		Messages: []client.Message{
			{Role: client.RoleSystem, Content: "system"},
			{Role: client.RoleAssistant, Content: strings.Repeat("prior work ", 2_500)},
			{Role: client.RoleUser, Content: "inspect"},
		},
		ContextPolicy: memory.Policy{ContextWindow: 16_000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07},
	}, events)

	compactions := 0
	for event := range events {
		if err := event.Err(); err != nil {
			t.Fatal(err)
		}
		if _, ok := event.Compaction(); ok {
			compactions++
		}
	}
	if compactions != 1 || len(requests) < 3 {
		t.Fatalf("compactions=%d requests=%d", compactions, len(requests))
	}

	compactIdx := -1
	for i, req := range requests {
		if isInPrefixCheckpointRequest(req) {
			compactIdx = i
			break
		}
	}
	if compactIdx < 1 || compactIdx+1 >= len(requests) {
		t.Fatalf("expected in-prefix compaction before next turn, requests=%#v", requests)
	}
	compactReq := requests[compactIdx]
	if compactReq.ConversationID != "thread-before-compact" {
		t.Fatalf("in-prefix compaction did not reuse ConversationID: got %q", compactReq.ConversationID)
	}
	if len(compactReq.Tools) == 0 || len(compactReq.Tools) != len(requests[0].Tools) {
		t.Fatalf("in-prefix compaction did not preserve Tools: got %d want %d", len(compactReq.Tools), len(requests[0].Tools))
	}
	prevMsgs := requests[compactIdx-1].Messages
	if len(compactReq.Messages) <= len(prevMsgs) {
		t.Fatalf("in-prefix messages (%d) did not retain prior prefix (%d)", len(compactReq.Messages), len(prevMsgs))
	}
	for i := range prevMsgs {
		if compactReq.Messages[i].Role != prevMsgs[i].Role || compactReq.Messages[i].Content != prevMsgs[i].Content {
			t.Fatalf("prefix diverged at index %d: got %#v want %#v", i, compactReq.Messages[i], prevMsgs[i])
		}
	}
	resumedReq := requests[compactIdx+1]
	if resumedReq.ConversationID != "" {
		t.Fatalf("resumed post-compaction request reused old ConversationID: %q", resumedReq.ConversationID)
	}
}

func TestInPrefixCompactionFallsBackToDetached(t *testing.T) {
	for _, reason := range []string{"tool_call", "invalid_json", "context_limit"} {
		t.Run(reason, func(t *testing.T) {
			var requests []client.ChatRequest
			chatClient := funcChatClient(func(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
				requests = append(requests, request)
				if isInPrefixCheckpointRequest(request) {
					switch reason {
					case "tool_call":
						return toolResponse("read_file"), nil
					case "invalid_json":
						return finalResponse("not valid json at all"), nil
					case "context_limit":
						return nil, &client.IncompleteGeneration{Reason: "context_limit"}
					}
				}
				if isDetachedCheckpointRequest(request) {
					body, _ := json.Marshal(memory.Checkpoint{ActiveWork: []string{"detached fallback"}})
					return finalResponse(string(body)), nil
				}
				return finalResponse("unexpected"), nil
			})

			loop := agentloop.NewContext(
				memory.Policy{ContextWindow: 32_000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07},
				[]client.Message{
					{Role: client.RoleSystem, Content: "system"},
					{Role: client.RoleAssistant, Content: strings.Repeat("work ", 15_000)},
					{Role: client.RoleUser, Content: "continue"},
				},
				[]client.Tool{{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "read_file"}}},
			)
			compaction, err := loop.CompactIfNeeded(t.Context(), chatClient, "test", "")
			if err != nil || compaction == nil {
				t.Fatalf("compaction=%#v err=%v", compaction, err)
			}
			if len(requests) != 2 || !isInPrefixCheckpointRequest(requests[0]) || !isDetachedCheckpointRequest(requests[1]) {
				t.Fatalf("expected in-prefix then detached fallback, got requests=%#v", requests)
			}
			if !strings.Contains(compaction.Summary, "detached fallback") {
				t.Fatalf("summary = %q", compaction.Summary)
			}
		})
	}
}

func TestCompactAfterLimitUsesDetachedOnly(t *testing.T) {
	var requests []client.ChatRequest
	chatClient := funcChatClient(func(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		requests = append(requests, request)
		body, _ := json.Marshal(memory.Checkpoint{ActiveWork: []string{"recovered after limit"}})
		return finalResponse(string(body)), nil
	})
	loop := agentloop.NewContext(
		memory.Policy{ContextWindow: 16_000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07},
		[]client.Message{
			{Role: client.RoleSystem, Content: "system"},
			{Role: client.RoleAssistant, Content: strings.Repeat("work ", 2_500)},
			{Role: client.RoleUser, Content: "next"},
		},
		[]client.Tool{{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "read_file"}}},
	)
	compaction, err := loop.CompactAfterLimit(t.Context(), chatClient, "test", "")
	if err != nil || compaction == nil {
		t.Fatalf("compaction=%#v err=%v", compaction, err)
	}
	if len(requests) != 1 || !isDetachedCheckpointRequest(requests[0]) {
		t.Fatalf("CompactAfterLimit must skip in-prefix and use detached directly: %#v", requests)
	}
}

func TestCompactBeforeAppendDuringPendingToolCallUsesDetached(t *testing.T) {
	var requests []client.ChatRequest
	chatClient := funcChatClient(func(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		requests = append(requests, request)
		body, _ := json.Marshal(memory.Checkpoint{ActiveWork: []string{"pending batch detached"}})
		return finalResponse(string(body)), nil
	})
	loop := agentloop.NewContext(
		memory.Policy{ContextWindow: 16_000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07},
		[]client.Message{
			{Role: client.RoleSystem, Content: "system"},
			{Role: client.RoleAssistant, Content: strings.Repeat("work ", 5_000)},
			{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "c1", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "read_file", Arguments: "{}"}}}},
		},
		[]client.Tool{{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "read_file"}}},
	)
	compaction, err := loop.CompactBeforeAppend(t.Context(), chatClient, "test", "", client.Message{
		Role: client.RoleTool, ToolCallID: "c1", Name: "read_file", Content: strings.Repeat("y", 20_000),
	})
	if err != nil || compaction == nil {
		t.Fatalf("compaction=%#v err=%v", compaction, err)
	}
	if len(requests) != 1 || !isDetachedCheckpointRequest(requests[0]) {
		t.Fatalf("pending tool call must fall back to detached compaction: %#v", requests)
	}
}
