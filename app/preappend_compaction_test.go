package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/workspace"
)

func preAppendChatModel(t *testing.T) (model, *fakeClient, string) {
	t.Helper()
	fake := &fakeClient{}
	value := config.Default()
	value.Provider.Model = "test-model"
	value.Provider.ContextWindow = 16000
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	store := workspace.Store{Root: t.TempDir()}
	m.workspaceStore = &store
	m.enterChat(value, fake)
	history := []client.Message{{Role: client.RoleSystem, Content: "Keep the contract."},
		{Role: client.RoleAssistant, Content: strings.Repeat("x", 24000)}}
	m.memory = memory.New(memoryPolicy(value), history)
	m.messages = append([]client.Message(nil), history...)
	m.conversationID = "old-conversation"
	incoming := "NEW_REQUEST:" + strings.Repeat("y", 18000)
	if m.memory.ShouldCompact() || !m.memory.ShouldCompactAfterAppend(client.Message{Role: client.RoleUser, Content: incoming}) {
		t.Fatal("fixture must cross the threshold only after the new request")
	}
	return m, fake, incoming
}

func TestChatCompactsBeforeAppendingUserRequest(t *testing.T) {
	m, fake, incoming := preAppendChatModel(t)
	original := m.memory.Messages()
	updated, command := m.startChatTurn(incoming, true)
	m = updated.(model)
	if !m.compacting || !m.pendingMessageDeferred || !reflect.DeepEqual(m.memory.Messages(), original) {
		t.Fatal("request entered context before compaction")
	}
	batch, ok := command().(tea.BatchMsg)
	if !ok {
		t.Fatal("missing compaction command")
	}
	var result compactionResultMsg
	for _, child := range batch {
		if message, ok := child().(compactionResultMsg); ok {
			result = message
		}
	}
	if len(fake.requests) != 1 || !isCheckpointRequestForTest(fake.requests[0]) || result.plan.ReservedTokens == 0 {
		t.Fatal("expected a checkpoint request reserving the new input")
	}
	for _, message := range fake.requests[0].Messages {
		if strings.Contains(message.Content, "NEW_REQUEST:") {
			t.Fatal("new request was summarized before being delivered")
		}
	}
	updated, command = m.Update(result)
	m = updated.(model)
	if m.turnErr != nil || m.compacting || m.pendingMessageDeferred || m.memory.ShouldCompact() || command == nil {
		t.Fatalf("compaction did not leave room for input: err=%v predicted=%d", m.turnErr, m.memory.PredictedTokens())
	}
	history := m.memory.Messages()
	if history[len(history)-1].Content != incoming || m.conversationID != "" || len(m.messages) != len(original)+1 {
		t.Fatal("pending request or original transcript was changed")
	}
	saved, err := m.workspaceStore.Load()
	if err != nil || len(saved.Context) != len(workspaceSessionMessages(history)) || !hasMessageNamed(saved.Context, memory.SummaryName) || saved.Context[len(saved.Context)-1].TextContent() != incoming {
		t.Fatalf("reserved context was not persisted: %v", err)
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if m.turnErr != nil || len(fake.requests) != 2 || fake.requests[1].ConversationID != "" {
		t.Fatalf("pending request did not continue: %v", m.turnErr)
	}
	request := fake.requests[1].Messages
	if request[len(request)-1].Content != incoming {
		t.Fatal("provider did not receive the exact new input")
	}
}

func TestPreAppendChatCompactionFailureKeepsHistoryAndRetry(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider_failure", true: "oversized_checkpoint"}[oversized], func(t *testing.T) {
			m, _, incoming := preAppendChatModel(t)
			original := m.memory.Messages()
			updated, _ := m.startChatTurn(incoming, true)
			m = updated.(model)
			plan, err := m.memory.PlanBeforeAppend(memory.Retention{PreserveInstructions: true, AllowTargetGrowth: true, SummarizeOversizedRecent: true}, m.pendingMessage)
			if err != nil {
				t.Fatal(err)
			}
			result := compactionResultMsg{turnID: m.turnID, plan: plan, err: errors.New("checkpoint failed")}
			if oversized {
				result.err = nil
				result.checkpoint = testCheckpointJSON(strings.Repeat("z", 30000))
			}
			updated, _ = m.Update(result)
			m = updated.(model)
			if m.turnErr == nil || m.waiting || m.compacting || m.pendingMessageDeferred || m.input.Value() != incoming {
				t.Fatalf("failure did not restore retry: err=%v waiting=%v", m.turnErr, m.waiting)
			}
			if !reflect.DeepEqual(m.memory.Messages(), original) || !reflect.DeepEqual(m.messages, original) || m.conversationID != "old-conversation" {
				t.Fatal("failed checkpoint mutated original history")
			}
		})
	}
}

func TestCancelledPreAppendChatIgnoresLateCheckpoint(t *testing.T) {
	m, _, incoming := preAppendChatModel(t)
	original := m.memory.Messages()
	updated, _ := m.startChatTurn(incoming, true)
	m = updated.(model)
	turnID := m.turnID
	updated, _ = m.interruptTurn()
	m = updated.(model)
	updated, command := m.Update(compactionResultMsg{turnID: turnID, checkpoint: testCheckpointJSON("late checkpoint")})
	m = updated.(model)
	if command != nil || m.waiting || m.pendingMessageDeferred || !reflect.DeepEqual(m.memory.Messages(), original) {
		t.Fatal("cancelled compaction changed model context")
	}
	if m.messages[len(m.messages)-1].Content != incoming {
		t.Fatal("cancelled request disappeared from the transcript")
	}
}
