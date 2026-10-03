package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/thinker"
	"github.com/snowmerak/q/workspace"
)

func testLiveSession(t *testing.T, configured chatClient) (*liveSession, *SessionHost) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	store := workspace.Store{Root: t.TempDir(), SessionID: "live-session"}
	settings := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test-model"
	value.Provider.ContextWindow = 1_000_000
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	state := newModel(ctx, settings, nil)
	state.workspaceStore = &store
	state.enterChat(value, configured)
	prepared := &preparedSession{state: state, store: store, client: configured, lifecycle: newStartupLifecycle()}
	session := newLiveSession(ctx, cancel, prepared)
	host := &SessionHost{ctx: t.Context(), store: settings, liveSessions: map[string]*liveSession{liveSessionKey(store, store.SessionID): session}}
	var err error
	session.configuration, err = host.sessionConfiguration(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.closeLiveSessions(); err != nil {
			t.Error(err)
		}
	})
	return session, host
}

type drainingLearnerClient struct {
	fakeClient
	started      chan struct{}
	stopping     chan struct{}
	allowExit    chan struct{}
	closedClient chan struct{}
}

func (configured *drainingLearnerClient) ListModels(ctx context.Context) ([]client.Model, error) {
	close(configured.started)
	<-ctx.Done()
	close(configured.stopping)
	<-configured.allowExit
	return nil, ctx.Err()
}

func (configured *drainingLearnerClient) Close() error {
	close(configured.closedClient)
	return nil
}

func TestLiveSessionCloseWaitsForBackgroundCommands(t *testing.T) {
	configured := &drainingLearnerClient{started: make(chan struct{}), stopping: make(chan struct{}), allowExit: make(chan struct{}), closedClient: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	state := newModel(ctx, config.Store{Dir: t.TempDir()}, nil)
	state.enterChat(config.Default(), configured)
	if err := state.ensureLearningMachine(thinker.LearningState{}, nil); err != nil {
		t.Fatal(err)
	}
	state.libraryClient = &qlibrary.Client{}
	state.learning.AppendMessage(client.Message{Role: client.RoleUser, Content: "learn from this turn"})
	state.learning.EnqueueExplicit()
	prepared := &preparedSession{state: state, lifecycle: newStartupLifecycle(), client: configured}
	session := newLiveSession(ctx, cancel, prepared)
	t.Cleanup(func() { _ = session.close() })
	t.Cleanup(func() { close(configured.allowExit) })
	select {
	case <-configured.started:
	case <-time.After(5 * time.Second):
		t.Fatal("restored learning queue did not resume")
	}
	closed := make(chan error, 1)
	go func() { closed <- session.close() }()
	select {
	case <-configured.stopping:
	case <-time.After(5 * time.Second):
		t.Fatal("session close did not cancel learning")
	}
	select {
	case <-configured.closedClient:
		t.Fatal("client closed before learner exited")
	case <-session.done:
		t.Fatal("session released its resources before learner exited")
	default:
	}
	// Release the blocked learner before cleanup; ownership must remain with
	// the session until every command that could write a checkpoint returns.
	configured.allowExit <- struct{}{}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session close did not drain learning")
	}
}

func TestLiveSessionLearningToggleRetainsProviderState(t *testing.T) {
	session, host := testLiveSession(t, &fakeClient{})
	if _, err := session.execute(t.Context(), "first", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := session.prepared.store.SaveLearningConfig(workspace.LearningConfig{Version: workspace.LearningConfigVersion, Disabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := host.RefreshLearning(t.Context(), session.prepared.store); err != nil {
		t.Fatal(err)
	}
	leased, err := host.acquireLiveSession(session.prepared.store, session.prepared.store.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if leased != session {
		t.Fatal("learning toggle recreated provider state")
	}
	leased.busy.Unlock()
	if err := session.close(); err != nil {
		t.Fatal(err)
	}
	if !session.prepared.state.learningDisabled() || session.prepared.state.learningCtx != nil {
		t.Fatal("learning toggle did not stop the live learner")
	}
}

func TestLiveSessionIgnoresCancelledTurnEvents(t *testing.T) {
	state := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	state.turnID, state.waiting = 2, true
	var emitted, finished bool
	execution := sessionExecutionModel{state: state, keepAlive: true,
		emit:     func(SessionEvent) error { emitted = true; return nil },
		finished: func(model, error) { finished = true },
	}
	updated, command := execution.Update(agentEventMsg{turnID: 1, event: agentEvent{err: context.Canceled}})
	if emitted || finished || command != nil || !updated.(sessionExecutionModel).state.waiting {
		t.Fatal("late cancellation finished the next turn")
	}
}

func TestLiveSessionRetainsConversationCalibrationAndClientAcrossTurns(t *testing.T) {
	configured := &fakeClient{usage: client.Usage{PromptTokens: 5000, CompletionTokens: 100, PromptDetails: &client.TokenDetails{CachedTokens: 4000}}}
	session, host := testLiveSession(t, configured)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := session.execute(ctx, "first", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if configured.closed {
		t.Fatal("turn completion closed the model client")
	}
	leased, err := host.acquireLiveSession(session.prepared.store, session.prepared.store.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if leased != session {
		t.Fatal("next turn recreated the session")
	}
	var initialContext int
	if _, err := leased.execute(ctx, "second", false, func(event SessionEvent) error {
		if event.Type == "context_usage" && initialContext == 0 {
			initialContext = event.ContextUsed
		}
		return nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	leased.busy.Unlock()
	if len(configured.requests) != 2 || configured.requests[1].ConversationID != "conversation-1" {
		t.Fatalf("next request lost the provider conversation: %#v", configured.requests)
	}
	if initialContext < 5000 {
		t.Fatalf("next turn forgot provider token overhead: %d", initialContext)
	}
	if err := host.ReleaseSession(session.prepared.store, session.prepared.store.SessionID); err != nil {
		t.Fatal(err)
	}
	if !configured.closed {
		t.Fatal("releasing the session did not close the client")
	}
}

func TestLiveSessionPersistenceFailureDoesNotKillNextTurn(t *testing.T) {
	configured := &fakeClient{}
	session, _ := testLiveSession(t, configured)
	want := errors.New("snapshot destination is locked")
	_, err := session.execute(t.Context(), "first", false, func(event SessionEvent) error {
		if event.Type == "result" {
			return want
		}
		return nil
	}, nil)
	if !errors.Is(err, want) {
		t.Fatalf("persistence failure = %v", err)
	}
	if _, err := session.execute(t.Context(), "next", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if configured.closed || len(configured.requests) != 2 {
		t.Fatal("failed turn destroyed the session runtime")
	}
}

func TestLiveSessionCancellationKeepsRuntimeAndClearsForegroundState(t *testing.T) {
	configured := &blockingSessionClient{started: make(chan struct{})}
	session, host := testLiveSession(t, configured)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		session.busy.Lock()
		defer session.busy.Unlock()
		_, err := session.execute(ctx, "block", false, nil, nil)
		done <- err
	}()
	select {
	case <-configured.started:
	case <-time.After(5 * time.Second):
		t.Fatal("turn never started")
	}
	if err := host.ReleaseSession(session.prepared.store, session.prepared.store.SessionID); !errors.Is(err, workspace.ErrLocked) {
		t.Fatalf("active session release = %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled turn = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled turn did not release its foreground operation")
	}
	if err := host.ReleaseSession(session.prepared.store, session.prepared.store.SessionID); err != nil {
		t.Fatal(err)
	}
	if session.prepared.state.waiting {
		t.Fatal("cancelled foreground state remained active")
	}
}

func TestLiveSessionProcessesBackgroundLearningAfterTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	state := newModel(ctx, config.Store{Dir: t.TempDir()}, nil)
	store := workspace.Store{Root: t.TempDir(), SessionID: "learning-session"}
	state.workspaceStore = &store
	state.enterChat(config.Default(), &fakeClient{})
	state.thinkerBusy, state.thinkerJobID = true, "background-job"
	generation := state.sessionGeneration
	prepared := &preparedSession{state: state, store: store, lifecycle: newStartupLifecycle(), client: state.client}
	session := newLiveSession(ctx, cancel, prepared)
	t.Cleanup(func() { _ = session.close() })
	if _, err := session.execute(t.Context(), "first", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	session.program.Send(thinkerResultMsg{jobID: "background-job", sessionGeneration: generation, err: errors.New("diagnostic learning result")})
	// A second request is a barrier after the background result on the same
	// serialized event loop. The result must not be lost at turn completion.
	if _, err := session.execute(t.Context(), "second", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := session.close(); err != nil {
		t.Fatal(err)
	}
	if prepared.state.thinkerBusy || prepared.state.thinkerJobID != "" {
		t.Fatal("idle session stopped processing background learning")
	}
}

func TestLiveSessionConfigurationTracksSettingsButNotMessages(t *testing.T) {
	session, host := testLiveSession(t, &fakeClient{})
	before, err := host.sessionConfiguration(session.prepared.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.execute(t.Context(), "change transcript", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	after, err := host.sessionConfiguration(session.prepared.store)
	if err != nil || before != after {
		t.Fatal("saving a transcript invalidated live settings")
	}
	value, err := host.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	value.Provider.Model = "other-model"
	if err := host.store.Save(value); err != nil {
		t.Fatal(err)
	}
	after, err = host.sessionConfiguration(session.prepared.store)
	if err != nil || before == after {
		t.Fatal("model change was not detected")
	}
}

func TestLiveSessionManualCompactionRetainsSession(t *testing.T) {
	session, _ := testLiveSession(t, &fakeClient{})
	status, err := session.execute(t.Context(), "", true, nil, nil)
	if err != nil || !strings.Contains(status, "Nothing to compact") {
		t.Fatalf("empty compaction = %q, %v", status, err)
	}
	if _, err := session.execute(t.Context(), "after compaction", false, nil, nil); err != nil {
		t.Fatal(err)
	}
}
