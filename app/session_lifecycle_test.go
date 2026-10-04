package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/thinker"
	"github.com/snowmerak/q/workspace"
)

func requireSessionLease(t *testing.T, host *SessionHost, session *liveSession, want error) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		leased, err := host.acquireLiveSession(session.prepared.store, session.prepared.store.SessionID)
		if leased != nil {
			leased.busy.Unlock()
			if leased != session {
				err = errors.New("lease recreated the session")
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatalf("session lease = %v, want %v", err, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session lease blocked behind another lifecycle operation")
	}
}

func TestLiveSessionSlowReleaseDoesNotBlockOtherSessions(t *testing.T) {
	fast, host := testLiveSession(t, &fakeClient{})
	configured := &drainingLearnerClient{started: make(chan struct{}), stopping: make(chan struct{}), allowExit: make(chan struct{}), closedClient: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	state := newModel(ctx, config.Store{Dir: t.TempDir()}, nil)
	state.enterChat(config.Default(), configured)
	if err := state.ensureLearningMachine(thinker.LearningState{}, nil); err != nil {
		t.Fatal(err)
	}
	state.libraryClient = &qlibrary.Client{}
	state.learning.AppendMessage(client.Message{Role: client.RoleUser, Content: "background learning"})
	state.learning.EnqueueExplicit()
	store := workspace.Store{Root: t.TempDir(), SessionID: "slow-session"}
	slow := newLiveSession(ctx, cancel, &preparedSession{state: state, store: store, client: configured, lifecycle: newStartupLifecycle()})
	host.liveMu.Lock()
	host.liveSessions[liveSessionKey(store, store.SessionID)] = slow
	host.liveMu.Unlock()
	t.Cleanup(func() { close(configured.allowExit); _ = slow.close() })
	select {
	case <-configured.started:
	case <-time.After(5 * time.Second):
		t.Fatal("background learner did not start")
	}
	released := make(chan error, 1)
	go func() { released <- host.ReleaseSession(store, store.SessionID) }()
	select {
	case <-configured.stopping:
	case <-time.After(5 * time.Second):
		t.Fatal("release did not cancel learning")
	}
	requireSessionLease(t, host, fast, nil)
	requireSessionLease(t, host, slow, workspace.ErrLocked)
	if err := host.releaseIdleSessions(); !errors.Is(err, workspace.ErrLocked) {
		t.Fatalf("embedding transition overlapped a pending release: %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- host.closeLiveSessions() }()
	// The fast actor stopping is a barrier: shutdown has already snapshotted
	// pending operations and must still wait for the removed actor to drain.
	select {
	case <-fast.done:
	case <-time.After(5 * time.Second):
		t.Fatal("host shutdown did not cancel the other session")
	}
	select {
	case err := <-closed:
		t.Fatalf("host shutdown skipped a draining session: %v", err)
	case err := <-released:
		t.Fatalf("session release skipped its learner: %v", err)
	default:
	}
	configured.allowExit <- struct{}{}
	for _, done := range []<-chan error{released, closed} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("shutdown did not finish after the learner drained")
		}
	}
}

func TestLiveSessionSlowPreparationDoesNotBlockOtherSessions(t *testing.T) {
	fast, host := testLiveSession(t, &fakeClient{})
	slow, _ := testLiveSession(t, &fakeClient{})
	store := slow.prepared.store
	host.liveMu.Lock()
	host.liveSessions[liveSessionKey(store, store.SessionID)] = slow
	host.liveMu.Unlock()
	started, proceed := make(chan struct{}), make(chan struct{})
	var ready sync.Once
	host.SetSessionWorkspaceResolver(func(root string) (SessionWorkspaceContext, bool, error) {
		if root == store.Root {
			ready.Do(func() { close(started) })
			<-proceed
		}
		return SessionWorkspaceContext{}, false, nil
	})
	t.Cleanup(func() { close(proceed) })
	prepared := make(chan error, 1)
	go func() {
		leased, err := host.acquireLiveSession(store, store.SessionID)
		if leased != nil {
			leased.busy.Unlock()
		}
		prepared <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("configuration preparation did not start")
	}
	requireSessionLease(t, host, fast, nil)
	requireSessionLease(t, host, slow, workspace.ErrLocked)
	host.liveMu.Lock()
	operation := host.liveOperations[liveSessionKey(store, store.SessionID)]
	host.liveMu.Unlock()
	closed := make(chan error, 1)
	go func() { closed <- host.closeLiveSessions() }()
	select {
	case <-operation.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not cancel pending preparation")
	}
	select {
	case err := <-closed:
		t.Fatalf("shutdown skipped pending preparation: %v", err)
	default:
	}
	proceed <- struct{}{}
	select {
	case err := <-prepared:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pending preparation continued after shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled preparation did not finish")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish after preparation")
	}
}
