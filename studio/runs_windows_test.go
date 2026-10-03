//go:build windows

package studio

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/internal/fsopen"
	"github.com/snowmerak/q/workspace"
)

func TestStudioRunContinuesAfterSnapshotReaderCloses(t *testing.T) {
	root := t.TempDir()
	store, lock, err := workspace.CreateSession(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	runner := sessionRunnerFunc(func(ctx context.Context, _ workspace.Store, _ string, _ string, emit app.SessionEventSink) error {
		// An ordinary Windows reader excludes deletion. Keep it open beyond the
		// previous 70 ms retry budget to reproduce the failed snapshot exchange.
		reader, err := os.Open(filepath.Join(store.SessionDir(), studioLatestRunFile))
		if err != nil {
			return err
		}
		closed := make(chan error, 1)
		go func() {
			time.Sleep(120 * time.Millisecond)
			closed <- reader.Close()
		}()
		err = emit(app.SessionEvent{Type: "message", Role: "tool", Name: "task_start", Content: `{"started":true}`})
		closeErr := <-closed
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return emit(app.SessionEvent{Type: "result", Outcome: "succeeded", Content: "continued"})
	})
	service := newSessionRunService(t.Context(), runner)
	defer service.Close()
	run, err := service.start(root, store.SessionID, "work")
	if err != nil {
		t.Fatal(err)
	}
	service.wg.Wait()
	page := run.page(0, maximumRunEventPage)
	if page.Run.Status != "completed" || page.Run.Error != "" {
		t.Fatalf("run after reader closed = %#v", page.Run)
	}
	reloaded, err := loadLatestStudioRun(root, store)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.page(0, maximumRunEventPage); got.Run.Status != "completed" || got.Run.Error != "" {
		t.Fatalf("reloaded run = %#v", got.Run)
	}
}

func TestStudioRunSnapshotAllowsReplacementWhileRead(t *testing.T) {
	root := t.TempDir()
	store, lock, err := workspace.CreateSession(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	run, err := createStudioRun(root, store, "snapshot-reader")
	if err != nil {
		t.Fatal(err)
	}
	defer run.close()
	reader, err := fsopen.Open(filepath.Join(store.SessionDir(), studioLatestRunFile))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := run.append(app.SessionEvent{Type: "message", Role: "tool", Name: "task_start"}); err != nil {
		t.Fatalf("snapshot replacement with shared reader: %v", err)
	}
}
