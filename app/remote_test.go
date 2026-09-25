package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

func TestRemoteExecutionReturnsAskToUserUnavailableAndContinues(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "tool-model"
	configuredClient := &askingClient{}
	state := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	state.toolRuntime = &fakeAgentTools{}
	state.enterChat(value, configuredClient)
	state.input.SetValue("choose a color")
	updated, initial := state.submitChat()
	state = updated.(model)
	if initial == nil {
		t.Fatal("chat did not start")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var events []RemoteEvent
	final, err := tea.NewProgram(
		remoteExecutionModel{
			state: state, initial: initial, cancel: cancel,
			emit: func(event RemoteEvent) error {
				events = append(events, event)
				return nil
			},
		},
		tea.WithContext(ctx), tea.WithInput(nil), tea.WithoutRenderer(), tea.WithoutSignalHandler(),
	).Run()
	if err != nil {
		t.Fatal(err)
	}
	result := final.(remoteExecutionModel)
	if result.err != nil {
		t.Fatal(result.err)
	}
	var question, terminal bool
	var outcome string
	for _, event := range events {
		question = question || event.Type == "question"
		if event.Type == "result" {
			terminal = true
			outcome = event.Outcome
		}
	}
	if !question || !terminal || outcome != "succeeded" {
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

func TestRemoteDefaultLoopTreatsSlashTextAsAPrompt(t *testing.T) {
	state := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	state.toolRuntime = &fakeAgentTools{}
	state.enterChat(config.Default(), &fakeClient{})
	updated, command := state.startChatTurn("/new", true)
	state = updated.(model)
	if command == nil || !state.waiting {
		t.Fatal("remote prompt did not start the main model loop")
	}
	if len(state.messages) == 0 || state.messages[len(state.messages)-1].Content != "/new" {
		t.Fatalf("messages = %#v", state.messages)
	}
}

func TestRemoteHostReturnsBusyBeforeRuntimeInitialization(t *testing.T) {
	globalStore := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test-model"
	if err := globalStore.Save(value); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	sessionStore, lock, err := workspace.CreateSession(root, "test owner")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lock.Close(); err != nil {
			t.Error(err)
		}
	}()
	host, err := NewRemoteHost(t.Context(), globalStore)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	}()
	err = host.Run(t.Context(), workspace.Store{Root: root}, sessionStore.SessionID, "", "continue", nil)
	if !errors.Is(err, workspace.ErrLocked) {
		t.Fatalf("run error = %v, want ErrLocked", err)
	}
}

func TestRemoteHostReleasesSessionAfterStartupFailure(t *testing.T) {
	globalStore := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test-model"
	value.Provider.Managed = true
	if err := globalStore.Save(value); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	sessionStore, initialLock, err := workspace.CreateSession(root, "prepare")
	if err != nil {
		t.Fatal(err)
	}
	if err := initialLock.Close(); err != nil {
		t.Fatal(err)
	}
	host, err := NewRemoteHost(t.Context(), globalStore)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := host.Run(t.Context(), workspace.Store{Root: root}, sessionStore.SessionID, "", "continue", nil); err == nil {
		t.Fatal("run unexpectedly succeeded without a configured provider")
	}
	reacquired, err := workspace.AcquireSessionLock(root, sessionStore.SessionID, "verify release")
	if err != nil {
		t.Fatalf("session lock was not released: %v", err)
	}
	_ = reacquired.Close()
}
