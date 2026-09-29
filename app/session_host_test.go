package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

func TestSessionExecutionProjectsQuestionAndCompletes(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "tool-model"
	value.Provider.ContextWindow = 4000
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
	var question, terminal, contextUsage bool
	for _, event := range events {
		question = question || event.Type == "question"
		terminal = terminal || event.Type == "result"
		contextUsage = contextUsage || event.Type == "context_usage" && event.ContextUsed > 0 && event.ContextSize == 4000
	}
	if !question || !terminal || !contextUsage {
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

func TestSessionExecutionAnswersExactQuestionThroughControl(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "tool-model"
	configured := &askingClient{}
	state := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	state.toolRuntime = &fakeAgentTools{}
	state.enterChat(value, configured)
	updated, initial := state.startChatTurn("choose a color", true)
	state = updated.(model)
	control := NewSessionRunControl()
	events := make(chan SessionEvent, 32)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		final, err := tea.NewProgram(
			sessionExecutionModel{
				state: state, initial: initial, cancel: cancel, control: control,
				emit: func(event SessionEvent) error { events <- event; return nil },
			},
			tea.WithContext(ctx), tea.WithInput(nil), tea.WithoutRenderer(), tea.WithoutSignalHandler(),
		).Run()
		result := final.(sessionExecutionModel)
		result.err = errors.Join(result.err, err)
		control.finish(result.err)
		done <- result.err
	}()

	for {
		select {
		case event := <-events:
			if event.Type != "question" {
				continue
			}
			if event.CallID != "ask-1" || event.Question != "Which color?" {
				t.Fatalf("question event = %#v", event)
			}
			if err := control.Answer(ctx, "blue"); err != nil {
				t.Fatal(err)
			}
			goto answered
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}

answered:
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var resumed strings.Builder
	if len(configured.requests) >= 2 {
		for _, message := range configured.requests[1].Messages {
			resumed.WriteString(message.Content)
		}
	}
	if len(configured.requests) < 2 || !strings.Contains(resumed.String(), `"selected_choice_id":"blue"`) {
		t.Fatalf("requests = %#v", configured.requests)
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

func TestSessionExecutionPauseResumeAndCancel(t *testing.T) {
	configured := &blockingSessionClient{started: make(chan struct{})}
	state := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	state.toolRuntime = &fakeAgentTools{}
	value := config.Default()
	value.Provider.Model = "tool-model"
	state.enterChat(value, configured)
	updated, initial := state.startChatTurn("work until stopped", true)
	state = updated.(model)
	control := NewSessionRunControl()
	events := make(chan SessionEvent, 16)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		final, err := tea.NewProgram(
			sessionExecutionModel{
				state: state, initial: initial, cancel: cancel, control: control,
				emit: func(event SessionEvent) error { events <- event; return nil },
			},
			tea.WithContext(ctx), tea.WithInput(nil), tea.WithoutRenderer(), tea.WithoutSignalHandler(),
		).Run()
		result := final.(sessionExecutionModel)
		result.err = errors.Join(result.err, err)
		control.finish(result.err)
		done <- result.err
	}()
	select {
	case <-configured.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := control.Pause(ctx); err != nil {
		t.Fatal(err)
	}
	if err := control.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if err := control.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var paused, resumed, cancelled bool
	for {
		select {
		case event := <-events:
			paused = paused || event.Type == "control" && event.Action == "paused"
			resumed = resumed || event.Type == "control" && event.Action == "resumed"
			cancelled = cancelled || event.Type == "cancelled"
		default:
			if !paused || !resumed || !cancelled {
				t.Fatalf("control events = paused %v resumed %v cancelled %v", paused, resumed, cancelled)
			}
			return
		}
	}
}

type blockingSessionClient struct {
	started chan struct{}
	once    sync.Once
}

func (configured *blockingSessionClient) Chat(ctx context.Context, _ client.ChatRequest) (*client.ChatResponse, error) {
	configured.once.Do(func() { close(configured.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*blockingSessionClient) ListModels(context.Context) ([]client.Model, error) { return nil, nil }
func (*blockingSessionClient) Close() error                                       { return nil }

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
