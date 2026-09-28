package app

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

func TestSessionExecutionProjectsQuestionAndCompletes(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "tool-model"
	state := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	state.toolRuntime = &fakeAgentTools{}
	state.enterChat(value, &askingClient{})
	updated, initial := state.startChatTurn("choose a color", true)
	state = updated.(model)
	if initial == nil {
		t.Fatal("chat did not start")
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var events []SessionEvent
	final, err := tea.NewProgram(
		sessionExecutionModel{
			state: state, initial: initial, cancel: cancel,
			emit: func(event SessionEvent) error {
				events = append(events, event)
				return nil
			},
		},
		tea.WithContext(ctx), tea.WithInput(nil), tea.WithoutRenderer(), tea.WithoutSignalHandler(),
	).Run()
	if err != nil {
		t.Fatal(err)
	}
	result := final.(sessionExecutionModel)
	if result.err != nil {
		t.Fatal(result.err)
	}
	var question, terminal bool
	for _, event := range events {
		question = question || event.Type == "question"
		terminal = terminal || event.Type == "result"
	}
	if !question || !terminal {
		t.Fatalf("events = %#v", events)
	}
	var unavailable bool
	for _, message := range result.state.messages {
		if message.Name == askToUserToolName && strings.Contains(message.Content, "interactive input is unavailable") {
			unavailable = true
		}
	}
	if !unavailable {
		t.Fatalf("ask_to_user unavailable result missing from %#v", result.state.messages)
	}
}

func TestSessionDefaultLoopTreatsSlashTextAsPrompt(t *testing.T) {
	state := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	state.toolRuntime = &fakeAgentTools{}
	state.enterChat(config.Default(), &fakeClient{})
	updated, command := state.startChatTurn("/new", true)
	state = updated.(model)
	if command == nil || !state.waiting {
		t.Fatal("session prompt did not start the default loop")
	}
	if len(state.messages) == 0 || state.messages[len(state.messages)-1].Content != "/new" {
		t.Fatalf("messages = %#v", state.messages)
	}
}

func TestSessionCompactionModelRunsManualCheckpoint(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "tool-model"
	value.Provider.ContextWindow = 4000
	store := workspace.Store{Root: t.TempDir()}
	state := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	state.workspaceStore = &store
	state.enterChat(value, &fakeClient{})
	for _, message := range compactCommandHistory() {
		state.messages = append(state.messages, message)
		state.memory.Append(message)
	}
	updated, initial := state.startManualCompaction()
	state = updated.(model)
	if initial == nil || !state.waiting {
		t.Fatal("manual compaction did not start")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	final, err := tea.NewProgram(
		sessionCompactionModel{state: state, initial: initial, cancel: cancel},
		tea.WithContext(ctx), tea.WithInput(nil), tea.WithoutRenderer(), tea.WithoutSignalHandler(),
	).Run()
	if err != nil {
		t.Fatal(err)
	}
	result := final.(sessionCompactionModel)
	if result.err != nil || result.state.waiting || !strings.Contains(result.state.status, "Context compacted") {
		t.Fatalf("compaction result = err %v, waiting %v, status %q", result.err, result.state.waiting, result.state.status)
	}
}
