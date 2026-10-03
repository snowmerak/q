package studio

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/workspace"
)

func TestStudioRunPreservesFailureDetails(t *testing.T) {
	for _, test := range []struct {
		name       string
		failure    error
		shutdown   bool
		status     string
		wantDetail string
	}{
		{"failure", errors.New("ReplaceFileW: access denied"), false, "failed", "ReplaceFileW: access denied"},
		{"failure with cancellation", errors.Join(context.Canceled, errors.New("ReplaceFileW: access denied")), false, "failed", "ReplaceFileW: access denied"},
		{"internal cancellation", context.Canceled, false, "failed", "context canceled"},
		{"shutdown", context.Canceled, true, "cancelled", "Studio is shutting down"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store, lock, err := workspace.CreateSession(root, "test")
			if err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			var service *sessionRunService
			runner := sessionRunnerFunc(func(ctx context.Context, _ workspace.Store, _ string, _ string, emit app.SessionEventSink) error {
				if test.shutdown {
					service.cancel()
				}
				return test.failure
			})
			service = newSessionRunService(t.Context(), runner)
			defer service.Close()
			run, err := service.start(root, store.SessionID, "work")
			if err != nil {
				t.Fatal(err)
			}
			service.wg.Wait()
			for _, reload := range []bool{false, true} {
				if reload {
					run, err = loadLatestStudioRun(root, store)
					if err != nil {
						t.Fatal(err)
					}
				}
				page := run.page(0, maximumRunEventPage)
				if page.Run.Status != test.status || !strings.Contains(page.Run.Error, test.wantDetail) || !strings.Contains(page.Run.Error, test.failure.Error()) {
					t.Fatalf("reloaded=%v: run = %#v", reload, page.Run)
				}
				if !test.shutdown && strings.Contains(page.Run.Error, "Studio is shutting down") {
					t.Fatalf("internal failure mislabeled as shutdown: %s", page.Run.Error)
				}
			}
		})
	}
}

func TestStudioRunKeepsEventFailureWhenRunnerReturnsCancellation(t *testing.T) {
	root := t.TempDir()
	store, lock, err := workspace.CreateSession(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	runner := sessionRunnerFunc(func(ctx context.Context, _ workspace.Store, _ string, _ string, emit app.SessionEventSink) error {
		if err := emit(app.SessionEvent{Type: "message", Content: strings.Repeat("x", maximumRunEventSize)}); err == nil {
			t.Error("oversized event unexpectedly persisted")
		}
		return context.Canceled
	})
	service := newSessionRunService(t.Context(), runner)
	defer service.Close()
	run, err := service.start(root, store.SessionID, "work")
	if err != nil {
		t.Fatal(err)
	}
	service.wg.Wait()
	page := run.page(0, maximumRunEventPage)
	if page.Run.Status != "failed" || !strings.Contains(page.Run.Error, "persist Studio message event") || !strings.Contains(page.Run.Error, "Studio run event exceeds") {
		t.Fatalf("failure = %#v", page.Run)
	}
}
