package agentloop_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
)

func TestAllowedToolsEnforcesOrdinaryAndLoopOwnedCalls(t *testing.T) {
	runtime := &scriptedTools{names: []string{"allowed", "run_command"}, content: "ok"}
	model := &scriptedClient{}
	model.chat = func(request client.ChatRequest) *client.ChatResponse {
		if len(request.Tools) != 2 {
			t.Errorf("catalog has %d tools", len(request.Tools))
		}
		for _, tool := range request.Tools {
			if tool.Function.Name != "allowed" && tool.Function.Name != "ask_to_user" {
				t.Errorf("unexpected %s", tool.Function.Name)
			}
		}
		if len(model.requests) > 1 {
			return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, Content: "done"}}}}
		}
		response := toolResponse("allowed")
		calls := []client.ToolCall{}
		for i, name := range []string{"allowed", "run_command", "task_start", memory.RecordFactTool, "allowed"} {
			calls = append(calls, client.ToolCall{ID: fmt.Sprint(i), Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: name, Arguments: `{}`}})
		}
		response.Choices[0].Message.ToolCalls = calls
		return response
	}
	events := make(chan agentloop.Event)
	go agentloop.RunAgentLoop(t.Context(), agentloop.Request{Client: model, Tools: runtime, Model: "test", AllowedTools: []string{"ask_to_user", "allowed"}, Messages: []client.Message{{Role: client.RoleUser, Content: "go"}}}, events)
	rejected := 0
	for event := range events {
		if err := event.Err(); err != nil {
			t.Fatal(err)
		}
		if message, ok := event.Message(); ok && message.Role == client.RoleTool && event.MessageIsToolError() {
			rejected++
		}
	}
	if rejected != 3 || len(runtime.calls) != 2 {
		t.Fatalf("rejected=%d executed=%+v", rejected, runtime.calls)
	}
	messages := agentloop.PrepareWorkspaceMessages(nil, agentloop.WorkspaceMessageOptions{Root: t.TempDir(), Tools: runtime, AllowedTools: []string{"ask_to_user", "allowed"}})
	for _, message := range messages {
		for _, forbidden := range []string{"task_start", "task_complete", "run_command", "loom_read"} {
			if strings.Contains(message.Content, forbidden) {
				t.Errorf("unauthorized instructions: %s", message.Content)
			}
		}
	}
}
