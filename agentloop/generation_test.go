package agentloop_test

import (
	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"testing"
)

func TestCutoffToolGenerationCannotDispatchEvenCompleteSiblingCalls(t *testing.T) {
	calls := 0
	configured := &scriptedClient{chat: func(request client.ChatRequest) *client.ChatResponse {
		calls++
		message := client.Message{Role: client.RoleAssistant}
		reason := "stop"
		switch calls {
		case 1:
			reason = "length"
			message.ToolCalls = []client.ToolCall{
				{ID: "complete-but-untrusted", Function: client.FunctionCall{Name: "write", Arguments: `{"value":1}`}},
				{ID: "cutoff", Function: client.FunctionCall{Name: "write", Arguments: `{"value":`}},
			}
		case 2:
			reason = "tool_calls"
			message.ToolCalls = []client.ToolCall{{ID: "verified", Function: client.FunctionCall{Name: "write", Arguments: `{"value":2}`}}}
		default:
			message.Content = "done"
		}
		return &client.ChatResponse{Choices: []client.Choice{{FinishReason: reason, Message: message}}}
	}}
	tools := &scriptedTools{name: "write", content: "written"}
	ctx := client.WithGenerationSettings(t.Context(), client.GenerationSettings{MaxTokens: 128, MaxAttempts: 2, SafeReplay: true, RequireFinishReason: true})
	events := make(chan agentloop.Event)
	go agentloop.RunAgentLoop(ctx, agentloop.Request{Client: configured, Tools: tools, Model: "test", Messages: []client.Message{{Role: client.RoleUser, Content: "write once"}}}, events)
	for event := range events {
		if err := event.Err(); err != nil {
			t.Fatal(err)
		}
	}
	if len(tools.calls) != 1 || tools.calls[0].ID != "verified" {
		t.Fatalf("dispatched=%+v", tools.calls)
	}
	if calls != 3 {
		t.Fatalf("generation calls=%d", calls)
	}
}
