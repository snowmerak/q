package agentloop_test

import (
	"encoding/json"
	"testing"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
)

func TestStreamAlternativeChoicesCannotCorruptSelectedResponse(t *testing.T) {
	call := func(id, name, args string) *client.Message {
		return &client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: id, Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: name, Arguments: args}}}}
	}
	configured := &scriptedClient{stream: &chunkStream{chunks: []*client.ChatChunk{
		{Raw: json.RawMessage(`{"choices":[{"index":1,"delta":{"refusal":"alternative refusal"}}]}`), Choices: []client.Choice{{Index: 1, Delta: call("other", "other_tool", `{`), FinishReason: "length"}}},
		{Raw: json.RawMessage(`{"choices":[{"index":1,"delta":{"reasoning_content":"alternative thought"}},{"index":0,"delta":{"reasoning_content":"selected thought"}}]}`), Choices: []client.Choice{{Index: 1, Delta: call("other", "other_tool", `}`)}, {Index: 0, Delta: call("selected", "lookup", `{"query":`)}}},
		{Choices: []client.Choice{{Index: 0, Delta: call("selected", "", `"healthcheck"}`), FinishReason: "tool_calls"}, {Index: 1, Delta: call("other", "other_tool", `{}`), FinishReason: "length"}}},
	}}}
	var thinking string
	response, _, err := agentloop.ConsumeChatStream(t.Context(), configured, client.ChatRequest{Model: "test"}, func(delta agentloop.AgentStreamDelta) bool {
		if delta.Kind == agentloop.AgentStreamThinking {
			thinking += delta.Content
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	message := response.Choices[0].Message
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].ID != "selected" || message.ToolCalls[0].Function.Name != "lookup" || message.ToolCalls[0].Function.Arguments != `{"query":"healthcheck"}` || message.Content != "" || thinking != "selected thought" {
		t.Fatalf("alternative completion corrupted selected response: %+v thinking=%q", message, thinking)
	}
	if err := client.CheckGeneration(response, true); err != nil {
		t.Fatal(err)
	}
}
