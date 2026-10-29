package studio

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

type managerDeliveryRunner struct {
	calls atomic.Int32
	ready chan struct{}
	end   chan struct{}
}

func (r *managerDeliveryRunner) Run(context.Context, workspace.Store, string, string, app.SessionEventSink) error {
	return errors.New("manager delivery bypassed operation controls")
}
func (r *managerDeliveryRunner) RunWithOptions(ctx context.Context, _ workspace.Store, id, _ string, options app.SessionOptions, emit app.SessionEventSink) error {
	if options.OperationID != "review-one" || options.Control == nil {
		return errors.New("manager operation identity or turn control was lost")
	}
	r.calls.Add(1)
	if err := emit(app.SessionEvent{Type: "question", SessionID: id, CallID: "question-one", Question: "Review this result?"}); err != nil {
		return err
	}
	close(r.ready)
	select {
	case <-r.end:
		return emit(app.SessionEvent{Type: "result", SessionID: id, Outcome: "succeeded"})
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestManagerDeliveryUsesMonitoredOperationAndSurvivesClear(t *testing.T) {
	store, lock, err := workspace.CreateSession(t.TempDir(), "manager")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	runner := &managerDeliveryRunner{ready: make(chan struct{}), end: make(chan struct{})}
	settings := config.Store{Dir: t.TempDir()}
	runs := newSessionRunService(t.Context(), runner)
	defer func() {
		if err := runs.Close(); err != nil {
			t.Error(err)
		}
	}()
	service := &sessionsService{main: settings, registry: newSessionRegistry(settings.Dir), runs: runs}
	finished := make(chan error, 1)
	go func() {
		finished <- service.deliverManagerOperation(t.Context(), store, "review-one", "Review the worker result")
	}()
	select {
	case <-runner.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("manager delivery did not start")
	}
	run, err := runs.latest(store.Root, store.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := run.snapshotCopy()
	if snapshot.Status != "waiting" || snapshot.OperationID != "review-one" || snapshot.PendingQuestion == nil {
		t.Fatalf("manager question was not visible to Studio: %+v", snapshot)
	}
	close(runner.end)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := runs.Close(); err != nil {
		t.Fatal(err)
	}
	if err := clearStudioRunArtifacts(store); err != nil {
		t.Fatal(err)
	}
	reopened := newSessionRunService(t.Context(), runner)
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	service.runs = reopened
	if err := service.deliverManagerOperation(t.Context(), store, "review-one", "Review the worker result"); err != nil {
		t.Fatal(err)
	}
	if runner.calls.Load() != 1 {
		t.Fatal("completed notification replayed after clearing conversation runs")
	}
	if err := service.deliverManagerOperation(t.Context(), store, "review-one", "Changed prompt"); err == nil {
		t.Fatal("a delivery receipt accepted different content")
	}
}
