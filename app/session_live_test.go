package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"

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

type drainingBackgroundClient struct {
	fakeClient
	started      chan struct{}
	stopping     chan struct{}
	allowExit    chan struct{}
	closedClient chan struct{}
}

func (configured *drainingBackgroundClient) ListModels(ctx context.Context) ([]client.Model, error) {
	close(configured.started)
	<-ctx.Done()
	close(configured.stopping)
	<-configured.allowExit
	return nil, ctx.Err()
}

func (configured *drainingBackgroundClient) Close() error {
	close(configured.closedClient)
	return nil
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
