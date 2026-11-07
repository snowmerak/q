package agentloop_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
)

func TestSkillRepeatedReadsReturnReferencesAndKeepOriginalPromptPrefix(t *testing.T) {
	const body = "Exact complete skill instructions used for this task."
	result, _ := json.Marshal(map[string]any{"skill": map[string]string{"id": "one", "digest": "v1"}, "path": "SKILL.md", "content": body})
	for _, preloaded := range []bool{false, true} {
		t.Run(map[bool]string{false: "parallel calls", true: "later turn"}[preloaded], func(t *testing.T) {
			runtime := &scriptedTools{name: "get_skill", content: string(result)}
			call := func(id string) client.ToolCall {
				return client.ToolCall{ID: id, Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "get_skill", Arguments: `{"id":"one"}`}}
			}
			initial := []client.Message{{Role: client.RoleUser, Content: "use the skill"}}
			calls := []client.ToolCall{call("first"), call("second")}
			if preloaded {
				initial = append(initial, client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{calls[0]}}, client.Message{Role: client.RoleTool, Name: "get_skill", ToolCallID: "first", Content: string(result)}, client.Message{Role: client.RoleUser, Content: "another task"})
				calls = calls[1:]
			}
			configured := &scriptedClient{}
			configured.chat = func(request client.ChatRequest) *client.ChatResponse {
				if len(configured.requests) == 1 {
					return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, ToolCalls: calls}}}}
				}
				return finalResponse("done")
			}
			events := make(chan agentloop.Event)
			go agentloop.RunAgentLoop(t.Context(), agentloop.Request{Client: configured, Tools: runtime, Model: "test", Messages: initial}, events)
			refs := 0
			for event := range events {
				if err := event.Err(); err != nil {
					t.Fatal(err)
				}
				if message, ok := event.Message(); ok && message.Role == client.RoleTool && message.ToolCallID == "second" {
					if strings.Contains(message.Content, body) || !strings.Contains(message.Content, `"previous_tool_call_id":"first"`) {
						t.Fatalf("duplicate body sent: %s", message.Content)
					}
					refs++
				}
			}
			if refs != 1 || len(configured.requests) != 2 {
				t.Fatalf("references=%d requests=%d", refs, len(configured.requests))
			}
			first, next := configured.requests[0].Messages, configured.requests[1].Messages
			if strings.Count(joined(next), body) != 1 {
				t.Fatal("original body was lost or duplicated in model context")
			}
			for i := range first {
				before, _ := json.Marshal(first[i])
				after, _ := json.Marshal(next[i])
				if string(before) != string(after) {
					t.Fatal("reusing a skill rewrote the existing prompt prefix")
				}
			}
		})
	}
}
