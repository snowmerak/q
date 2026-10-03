package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
	"github.com/snowmerak/q/workspace"
)

func seedCoveredHistory(t *testing.T, m *model) {
	t.Helper()
	appendMessage := func(message client.Message) {
		m.messages = append(m.messages, message)
		m.memory.Append(message)
	}
	appendMessage(client.Message{Role: client.RoleUser, Content: "Implement D only. Keep the exact constraints."})
	appendMessage(client.Message{Role: client.RoleAssistant, Content: strings.Repeat("covered investigation ", 500)})
	for _, input := range []struct{ name, arguments string }{
		{memory.RecordFactTool, `{"fact":"D is verified","source":"loom://checks"}`},
		{memory.CheckpointTool, `{"expected_revision":1}`},
	} {
		call := client.ToolCall{ID: input.name, Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: input.name, Arguments: input.arguments}}
		appendMessage(client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call}})
		result, handled := m.memory.CallMemoryTool(call)
		if !handled || result.IsError {
			t.Fatalf("memory tool: %s", result.Content)
		}
		appendMessage(client.ToolResultMessage(call, result))
	}
}

func TestTUICoveredCompactPersistsWithoutModel(t *testing.T) {
	for _, scenario := range []string{"save", "save failure", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			store := workspace.Store{Root: t.TempDir()}
			value := config.Default()
			value.Provider.Model, value.Provider.ContextWindow = "test-model", 20_000
			fake := &fakeClient{}
			m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
			m.workspaceStore = &store
			m.enterChat(value, fake)
			seedCoveredHistory(t, &m)
			m.conversationID = "old-backend"
			before, transcript := m.memory.Messages(), append([]client.Message(nil), m.messages...)
			if scenario == "save failure" {
				blocked := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(blocked, []byte("blocked"), 0o600); err != nil {
					t.Fatal(err)
				}
				m.workspaceStore = &workspace.Store{Root: blocked}
			}
			m.input.SetValue("/compact")
			updated, command := m.submitChat()
			m = updated.(model)
			batch, ok := command().(tea.BatchMsg)
			if !ok {
				t.Fatal("compaction did not start")
			}
			var result compactionResultMsg
			for _, child := range batch {
				if message, ok := child().(compactionResultMsg); ok {
					result = message
				}
			}
			if result.checkpoint == "" || result.response != nil || len(fake.requests) != 0 {
				t.Fatal("covered compaction called the model")
			}
			if scenario == "cancel" {
				updated, _ = m.interruptTurn()
				m = updated.(model)
			}
			updated, _ = m.Update(result)
			m = updated.(model)
			if !reflect.DeepEqual(transcript, m.messages) {
				t.Fatal("direct compaction changed full transcript")
			}
			if scenario != "save" {
				if !reflect.DeepEqual(before, m.memory.Messages()) || m.conversationID != "old-backend" {
					t.Fatal("failed or cancelled compaction mutated context")
				}
				return
			}
			saved, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			restored := mergeWorkspaceMessages(nil, saved.Context)
			if m.conversationID != "" || !hasMessageNamed(restored, memory.RequestAnchorName) || !hasMessageNamed(restored, memory.SummaryName) {
				t.Fatal("saved context lost original requests or checkpoint on reload")
			}
		})
	}
}

func TestACPCoveredCompactPersistsWithoutModel(t *testing.T) {
	fake := &fakeClient{}
	agent, store, _ := testACPAgent(t, fake, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, store.Root)
	runtime := activeACPRuntime(t, agent, sessionID)
	value := runtime.state.activeConfig()
	value.Provider.ContextWindow = 20_000
	runtime.state.config = value
	runtime.state.memory.Configure(memoryPolicy(value))
	seedCoveredHistory(t, runtime.state)
	before := len(runtime.state.messages)
	runtime.state.conversationID = "old-backend"
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock("/compact")}})
	if err != nil || response.StopReason != acp.StopReasonEndTurn || len(fake.requests) != 0 {
		t.Fatalf("direct compact: response=%#v err=%v requests=%d", response, err, len(fake.requests))
	}
	saved, err := activeACPWorkspaceStore(t, agent, sessionID).Load()
	if err != nil || !hasMessageNamed(saved.Context, memory.RequestAnchorName) || runtime.state.conversationID != "" || len(runtime.state.messages) != before {
		t.Fatalf("direct compact did not persist atomically: %v", err)
	}
}
