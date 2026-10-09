package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/workspace"
)

func TestRuntimeModelDoesNotInitializeOrUpdateTerminalControls(t *testing.T) {
	state := newRuntimeModel(t.Context(), config.Store{Dir: t.TempDir()}, nil, nil)
	state.workspaceLearning = workspace.LearningConfig{Disabled: true}
	state.workspaceLearningRestored = true
	state.enterSession(config.Default(), &fakeClient{})
	t.Cleanup(state.stopSessionLearning)
	updated, command := state.updateExecution(tea.WindowSizeMsg{Width: 150, Height: 60})
	state = updated.(model)
	if !state.headless || state.width != 0 || state.height != 0 || state.viewport.Width() != 0 ||
		state.input.Width() != 0 || command != nil || state.focusChatInput() != nil || state.chatTick() != nil {
		t.Fatal("execution initialized or updated terminal controls")
	}
	question := askToUserInput{Question: "Continue?"}
	updated, command = state.updateExecution(agentEventMsg{event: agentEvent{question: &question}})
	state = updated.(model)
	if !state.asking || state.pendingQuestion.Question != question.Question || command != nil {
		t.Fatal("headless question did not retain execution state")
	}
}

func TestSessionMessagePersistenceAcknowledgesStoredUsage(t *testing.T) {
	state := newRuntimeModel(t.Context(), config.Store{Dir: t.TempDir()}, nil, nil)
	store := workspace.Store{Root: t.TempDir()}
	state.workspaceStore = &store
	state.config = config.Default()
	state.memory = memory.New(memoryPolicy(state.config), nil)
	for index := range 2 {
		ack := make(chan struct{})
		message := client.Message{Role: client.RoleAssistant, Content: "tool request"}
		usage := client.Usage{PromptTokens: 20 + index, CompletionTokens: 2, TotalTokens: 22 + index}
		state.appendSessionMessage(message, &usage)
		if err := state.persistAgentMessage(agentEvent{message: &message, persistenceAck: ack}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ack:
		default:
			t.Fatal("saved message was not acknowledged")
		}
		stored, err := store.Load()
		if err != nil || len(stored.Transcript) != index+1 || len(stored.ResponseUsage) != index+1 ||
			stored.ResponseUsage[index].AssistantIndex != index || stored.ResponseUsage[index].InputTokens != 20+index {
			t.Fatalf("acknowledged projection does not contain message usage: %#v, %v", stored, err)
		}
	}
}

func TestSessionMessagePersistenceFailureCancelsBeforeAcknowledgment(t *testing.T) {
	root := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(root, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	state := newRuntimeModel(t.Context(), config.Store{Dir: t.TempDir()}, nil, nil)
	state.workspaceStore = &workspace.Store{Root: root}
	state.config = config.Default()
	state.memory = memory.New(memoryPolicy(state.config), nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	state.turnCancel = cancel
	ack := make(chan struct{})
	message := client.Message{Role: client.RoleTool, Content: "result"}
	if err := state.persistAgentMessage(agentEvent{message: &message, persistenceAck: ack}); err == nil {
		t.Fatal("invalid destination accepted a session save")
	}
	select {
	case <-ack:
		t.Fatal("failed save was acknowledged")
	default:
	}
	updated, _ := state.updateExecution(agentEventMsg{event: agentEvent{message: &message, persistenceAck: ack}})
	if updated.(model).turnErr == nil || ctx.Err() == nil {
		t.Fatal("failed save did not cancel execution")
	}
	select {
	case <-ack:
	default:
		t.Fatal("canceled producer was not released")
	}
}
