package agentloop_test

import (
	"errors"
	"testing"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
)

func TestStreamSparseToolIndicesMergeOnlyReceivedCalls(t *testing.T) {
	configured := &scriptedClient{stream: &chunkStream{chunks: []*client.ChatChunk{
		{Choices: []client.Choice{{Delta: &client.Message{ToolCalls: []client.ToolCall{{Index: 9, ID: "search", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "search_", Arguments: `{"que`}}}}}}},
		{Choices: []client.Choice{{Delta: &client.Message{ToolCalls: []client.ToolCall{{Index: 4, ID: "skill", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "get_skill", Arguments: `{"id":"one"}`}}}}}}},
		{Choices: []client.Choice{{Delta: &client.Message{ToolCalls: []client.ToolCall{{Index: 9, Function: client.FunctionCall{Name: "work", Arguments: `ry":"한국 대통령 일본 총리 2026"}`}}}}, FinishReason: "tool_calls"}}},
	}}}
	response, _, err := agentloop.ConsumeChatStream(t.Context(), configured, client.ChatRequest{Model: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := response.Choices[0].Message.ToolCalls
	if len(calls) != 2 || calls[0].Index != 4 || calls[0].ID != "skill" || calls[1].Index != 9 || calls[1].ID != "search" || calls[1].Function.Name != "search_work" || calls[1].Function.Arguments != `{"query":"한국 대통령 일본 총리 2026"}` {
		t.Fatalf("sparse or interleaved fragments created phantom calls: %+v", calls)
	}
	if err := client.CheckGeneration(response, true); err != nil {
		t.Fatal(err)
	}
}

func TestStreamRealMalformedSparseCallStillFailsValidation(t *testing.T) {
	configured := &scriptedClient{stream: &chunkStream{chunks: []*client.ChatChunk{{Choices: []client.Choice{{Delta: &client.Message{ToolCalls: []client.ToolCall{{Index: 17, ID: "malformed", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "search_work", Arguments: `{"query":`}}}}, FinishReason: "tool_calls"}}}}}}
	response, _, err := agentloop.ConsumeChatStream(t.Context(), configured, client.ChatRequest{Model: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Choices[0].Message.ToolCalls) != 1 {
		t.Fatal("missing index became a tool call")
	}
	var incomplete *client.IncompleteGeneration
	if err := client.CheckGeneration(response, true); !errors.As(err, &incomplete) || incomplete.Reason != "invalid_tool_arguments" {
		t.Fatalf("malformed received call was accepted: %v", err)
	}
}

func TestStreamDelegatedContinuationKeepsThreadAndExecutesEachCallOnce(t *testing.T) {
	configured := &completionScript{messages: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "skill", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "get_skill", Arguments: `{"id":"one"}`}}}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{Index: 1, ID: "search", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "search_work", Arguments: `{"query":"한국 대통령 일본 총리 2026"}`}}}},
		{Role: client.RoleAssistant, Content: "done"},
	}}
	runtime := &scriptedTools{names: []string{"get_skill", "search_work"}, content: `{}`}
	// Match Codex's no-replay policy: this must work without regenerating a turn.
	ctx := client.WithGenerationSettings(t.Context(), client.GenerationSettings{MaxAttempts: 0, RequireFinishReason: true})
	events := make(chan agentloop.Event)
	go agentloop.RunAgentLoop(ctx, agentloop.Request{Client: configured, Tools: runtime, Model: "test", Stream: true, ConversationID: "retained-thread", Messages: []client.Message{{Role: client.RoleUser, Content: "Check current leaders"}}}, events)
	var failure error
	for event := range events {
		if err := event.Err(); err != nil {
			failure = err
		}
	}
	if failure != nil {
		t.Fatal(failure)
	}
	if len(runtime.calls) != 2 || runtime.calls[0].ID != "skill" || runtime.calls[1].ID != "search" || len(configured.requests) != 3 {
		t.Fatalf("calls=%+v requests=%d", runtime.calls, len(configured.requests))
	}
	for _, request := range configured.requests {
		if request.ConversationID != "retained-thread" {
			t.Fatal("continuation reset the thread")
		}
	}
}
