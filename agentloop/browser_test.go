package agentloop_test

import (
	"strings"
	"testing"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
)

func TestBrowserPreferenceReachesExistingConversationOnlyWithCommandTool(t *testing.T) {
	for _, tool := range []string{"run_command", "lookup"} {
		t.Run(tool, func(t *testing.T) {
			configured := &scriptedClient{chat: func(client.ChatRequest) *client.ChatResponse { return finalResponse("done") }}
			messages := []client.Message{{Role: client.RoleSystem, Content: "stable instructions"}, {Role: client.RoleUser, Content: "browse"}}
			events := make(chan agentloop.Event)
			go agentloop.RunAgentLoop(t.Context(), agentloop.Request{Client: configured, Tools: &scriptedTools{name: tool}, Model: "test", ConversationID: "durable-thread", Messages: messages, BrowserPreference: "preferred browser CLI"}, events)
			for event := range events {
				if err := event.Err(); err != nil {
					t.Fatal(err)
				}
			}
			if len(configured.requests) != 1 {
				t.Fatal("unexpected request count")
			}
			request := configured.requests[0]
			if request.ConversationID != "durable-thread" || request.Messages[0].Content != "stable instructions" || messages[1].Content != "browse" {
				t.Fatal("thread or prompt prefix changed")
			}
			if strings.Contains(request.Messages[1].TextContent(), "preferred browser CLI") != (tool == "run_command") {
				t.Fatal("preference missing or unsupported tool advertised")
			}
		})
	}
}
