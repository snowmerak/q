package app

import (
	"context"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

func TestInterruptedChatTurnRestoresSavedMessageBoundaries(t *testing.T) {
	store := workspace.Store{Root: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test-model"
	newChat := func() model {
		m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
		m.workspaceStore = &store
		m.enterChat(value, &fakeClient{})
		return m
	}

	m := newChat()
	updated, _ := m.startChatTurn("finish the task", false)
	m = updated.(model)
	assertSavedTail := func(wantRole client.Role, wantContent string) {
		t.Helper()
		saved, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(saved.Transcript) == 0 || len(saved.Context) == 0 {
			t.Fatalf("missing checkpoint: %#v", saved)
		}
		for _, messages := range [][]client.Message{saved.Transcript, saved.Context} {
			last := messages[len(messages)-1]
			if last.Role != wantRole || last.Content != wantContent {
				t.Fatalf("saved tail = %#v, want %q %q", last, wantRole, wantContent)
			}
		}
	}
	assertSavedTail(client.RoleUser, "finish the task")
	promptRestart := newChat()
	if len(promptRestart.messages) == 0 || promptRestart.messages[len(promptRestart.messages)-1].Content != "finish the task" {
		t.Fatalf("submitted prompt was lost on restart: %#v", promptRestart.messages)
	}

	call := client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
		ID: "call-1", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "read_file", Arguments: `{"path":"main.go"}`},
	}}}
	updated, _ = m.updateAgentEvent(agentEventMsg{event: agentEvent{message: &call}, events: make(chan agentEvent)})
	m = updated.(model)
	result := client.Message{Role: client.RoleTool, Name: "read_file", ToolCallID: "call-1", Content: "file contents"}
	updated, _ = m.updateAgentEvent(agentEventMsg{event: agentEvent{message: &result}, events: make(chan agentEvent)})
	m = updated.(model)
	assertSavedTail(client.RoleTool, "file contents")

	restarted := newChat()
	if len(restarted.messages) < 3 || len(restarted.memory.Messages()) < 3 {
		t.Fatalf("incomplete restored conversation: %#v", restarted.messages)
	}
	last := restarted.messages[len(restarted.messages)-1]
	if last.ToolCallID != "call-1" || last.Content != "file contents" {
		t.Fatalf("restored tool result = %#v", last)
	}
	previous := restarted.messages[len(restarted.messages)-2]
	if len(previous.ToolCalls) != 1 || previous.ToolCalls[0].ID != "call-1" {
		t.Fatalf("restored tool call = %#v", previous)
	}
}

func TestRestartClosesInterruptedToolCallBeforeNextRequest(t *testing.T) {
	store := workspace.Store{Root: t.TempDir()}
	call := client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
		ID: "call-1", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "run_command", Arguments: `{"command":"write output"}`},
	}}}
	if err := store.Save(workspace.Session{
		Transcript: []client.Message{{Role: client.RoleUser, Content: "write output"}, call},
		Context:    []client.Message{{Role: client.RoleUser, Content: "write output"}, call},
	}); err != nil {
		t.Fatal(err)
	}

	value := config.Default()
	value.Provider.Model = "test-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &store
	m.enterChat(value, &fakeClient{})
	if !strings.Contains(m.status, "interrupted tool call recovered") {
		t.Fatalf("restore status = %q", m.status)
	}
	saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, messages := range [][]client.Message{saved.Transcript, saved.Context, m.memory.Messages()} {
		last := messages[len(messages)-1]
		if last.Role != client.RoleTool || last.ToolCallID != "call-1" || !strings.Contains(last.Content, "execution outcome unknown") {
			t.Fatalf("unreconciled tool call: %#v", messages)
		}
	}
	restarted := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	restarted.workspaceStore = &store
	restarted.enterChat(value, &fakeClient{})
	if len(restarted.messages) != len(m.messages) || strings.Contains(restarted.status, "interrupted tool call recovered") {
		t.Fatalf("restart recovery repeated: messages=%#v status=%q", restarted.messages, restarted.status)
	}
}

func TestResponsesCacheAffinitySurvivesRestartForSameModeAndModel(t *testing.T) {
	store := workspace.Store{Root: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test-model"
	value.ModelAPIModes = map[string]string{"test-model": "responses"}
	newChat := func(value config.Config) model {
		m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
		m.workspaceStore = &store
		m.enterChat(value, &fakeClient{})
		return m
	}
	m := newChat(value)
	m.conversationID = "cache_persisted-affinity"
	if err := m.saveWorkspaceSession(); err != nil {
		t.Fatal(err)
	}
	if got := newChat(value).conversationID; got != m.conversationID {
		t.Fatalf("Responses affinity after restart = %q", got)
	}

	chatValue := value
	chatValue.ModelAPIModes = map[string]string{"test-model": "chat_completions"}
	if got := newChat(chatValue).conversationID; got != "" {
		t.Fatalf("Chat mode inherited Responses affinity %q", got)
	}
	otherModel := value
	otherModel.Provider.Model = "other-model"
	if got := newChat(otherModel).conversationID; got != "" {
		t.Fatalf("other model inherited Responses affinity %q", got)
	}
}
