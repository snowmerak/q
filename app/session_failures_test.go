package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workspace"
)

type sessionFailureClient struct {
	fakeClient
	store         workspace.Store
	blockSave     bool
	checkpoint    string
	checkpointErr error
}

func (configured *sessionFailureClient) Chat(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	if isCheckpointRequestForTest(request) && configured.checkpointErr != nil {
		return nil, configured.checkpointErr
	}
	response, err := configured.fakeClient.Chat(ctx, request)
	if isCheckpointRequestForTest(request) && configured.checkpoint != "" {
		response.Choices[0].Message.Content = configured.checkpoint
	}
	if configured.blockSave {
		if err := os.MkdirAll(filepath.Dir(configured.store.Path()), 0700); err != nil {
			return nil, err
		}
		if err := os.Remove(configured.store.Path()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err := os.Mkdir(configured.store.Path(), 0700); err != nil {
			return nil, err
		}
	}
	return response, err
}

func testCompactingSession(t *testing.T, configured *sessionFailureClient) (*liveSession, []client.Message) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	state := newModel(ctx, config.Store{Dir: t.TempDir()}, nil)
	value := config.Default()
	value.Provider.Model, value.Provider.ContextWindow = "test-model", 8000
	store := workspace.Store{Root: t.TempDir(), SessionID: "compaction-session"}
	configured.store = store
	state.workspaceStore = &store
	state.enterChat(value, configured)
	history := []client.Message{
		{Role: client.RoleUser, Content: strings.Repeat("old facts ", 5000)},
		{Role: client.RoleAssistant, Content: strings.Repeat("old answer ", 100)},
		{Role: client.RoleUser, Content: "recent question"},
	}
	state.messages = append([]client.Message(nil), history...)
	state.memory = memory.New(memoryPolicy(value), history)
	if !state.memory.ShouldCompact() {
		t.Fatal("history does not trigger automatic compaction")
	}
	prepared := &preparedSession{state: state, store: store, client: configured, lifecycle: newStartupLifecycle()}
	session := newLiveSession(ctx, cancel, prepared)
	t.Cleanup(func() { _ = session.close() })
	return session, history
}

func TestLiveSessionCompactionFailuresFinishAndAllowRetry(t *testing.T) {
	for _, manual := range []bool{false, true} {
		for _, failure := range []string{"provider", "checkpoint", "save"} {
			name := "automatic/" + failure
			if manual {
				name = "manual/" + failure
			}
			t.Run(name, func(t *testing.T) {
				want := errors.New("checkpoint provider failed")
				configured := &sessionFailureClient{}
				switch failure {
				case "provider":
					configured.checkpointErr = want
				case "checkpoint":
					configured.checkpoint = "this is not a checkpoint"
				case "save":
					configured.blockSave = true
				}
				session, history := testCompactingSession(t, configured)
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				result := false
				compacting := false
				_, err := session.execute(ctx, "failed prompt", manual, func(event SessionEvent) error {
					result = result || event.Type == "result"
					compacting = compacting || event.Type == "status" && strings.Contains(event.Detail, "Compacting context")
					return nil
				}, nil)
				if err == nil || errors.Is(err, context.DeadlineExceeded) || result {
					t.Fatalf("compaction did not finish as failed: err=%v result=%v", err, result)
				}
				if !compacting {
					t.Fatal("Studio did not receive the compaction status")
				}
				if failure == "provider" && !errors.Is(err, want) {
					t.Fatalf("provider error was lost: %v", err)
				}
				if failure == "save" {
					var saveErr *workspaceSessionSaveError
					if !errors.As(err, &saveErr) {
						t.Fatalf("checkpoint persistence error was lost: %v", err)
					}
					if err := os.Remove(configured.store.Path()); err != nil {
						t.Fatal(err)
					}
				}
				configured.checkpointErr, configured.checkpoint, configured.blockSave = nil, "", false
				if _, err := session.execute(ctx, "retry prompt", false, nil, nil); err != nil {
					t.Fatalf("failed compaction prevented retry: %v", err)
				}
				if err := session.close(); err != nil {
					t.Fatal(err)
				}
				state := session.prepared.state
				if state.waiting || state.turnErr != nil || len(state.messages) != len(history)+2 ||
					!reflect.DeepEqual(state.messages[:len(history)], history) || state.messages[len(history)].Content != "retry prompt" {
					t.Fatal("compaction failure lost history, kept the failed prompt, or poisoned the next turn")
				}
			})
		}
	}
}

func TestLiveSessionFinalSaveFailureIsReportedAndAllowsRetry(t *testing.T) {
	configured := &sessionFailureClient{blockSave: true}
	session, _ := testLiveSession(t, configured)
	configured.store = session.prepared.store
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := false
	_, err := session.execute(ctx, "reply then fail saving", false, func(event SessionEvent) error {
		result = result || event.Type == "result"
		return nil
	}, nil)
	var saveErr *workspaceSessionSaveError
	if !errors.As(err, &saveErr) || result {
		t.Fatalf("failed final save was reported as successful: err=%v result=%v", err, result)
	}
	if err := os.Remove(configured.store.Path()); err != nil {
		t.Fatal(err)
	}
	configured.blockSave = false
	if _, err := session.execute(ctx, "next prompt", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	saved, err := configured.store.Load()
	if err != nil || len(saved.Transcript) != 4 {
		t.Fatalf("next turn did not persist retained history: %#v, %v", saved.Transcript, err)
	}
}

func TestLiveSessionSuccessfulManualCompactionDoesNotSendChat(t *testing.T) {
	configured := &sessionFailureClient{}
	session, history := testCompactingSession(t, configured)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	status, err := session.execute(ctx, "", true, nil, nil)
	if err != nil || !strings.Contains(status, "Context compacted") || len(configured.requests) != 1 || !isCheckpointRequestForTest(configured.requests[0]) {
		t.Fatalf("manual compaction did not finish independently: status=%q err=%v requests=%d", status, err, len(configured.requests))
	}
	if _, err := session.execute(ctx, "after compaction", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := session.close(); err != nil {
		t.Fatal(err)
	}
	if got := session.prepared.state.messages; len(got) != len(history)+2 || !reflect.DeepEqual(got[:len(history)], history) {
		t.Fatal("manual compaction altered the transcript")
	}
}

type sessionFailureArchive struct{ err error }

func (*sessionFailureArchive) Append(sessionstore.Record) error { return nil }
func (archive *sessionFailureArchive) Flush() error             { return archive.err }

func TestSessionExecutionArchiveFailureIsReported(t *testing.T) {
	state := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	state.enterChat(config.Default(), &fakeClient{})
	want := errors.New("archive flush failed")
	state.archive = &sessionFailureArchive{err: want}
	state.beginTurn()
	state.waiting = true
	var result bool
	execution := sessionExecutionModel{state: state, emit: func(event SessionEvent) error {
		result = result || event.Type == "result"
		return nil
	}}
	updated, _ := execution.Update(chatResultMsg{turnID: state.turnID,
		response: &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, Content: "reply"}}}},
	})
	if got := updated.(sessionExecutionModel); !errors.Is(got.err, want) || result || got.state.waiting {
		t.Fatalf("archive failure was reported as successful: err=%v result=%v", got.err, result)
	}
}

func TestLiveSessionIgnoresCancelledCompactionResult(t *testing.T) {
	state := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	state.turnID, state.waiting = 2, true
	var emitted, finished bool
	execution := sessionExecutionModel{state: state, keepAlive: true,
		emit:     func(SessionEvent) error { emitted = true; return nil },
		finished: func(model, error) { finished = true },
	}
	updated, command := execution.Update(compactionResultMsg{turnID: 1, err: context.Canceled})
	if emitted || finished || command != nil || !updated.(sessionExecutionModel).state.waiting {
		t.Fatal("cancelled compaction finished the next turn")
	}
}
