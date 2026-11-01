package studio

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

func createPagingTestRun(t *testing.T) *studioRun {
	t.Helper()
	root := t.TempDir()
	store, lock, err := workspace.CreateSession(root, "paging")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	run, err := createStudioRun(root, store, "paging-run")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := run.close(); err != nil {
			t.Error(err)
		}
	})
	return run
}

func TestStudioRunReplaysAndPagesLogLargerThan128MiB(t *testing.T) {
	run := createPagingTestRun(t)
	if err := run.append(app.SessionEvent{Type: "session", SessionID: run.store.SessionID}); err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("x", 4<<20)
	for i := range 33 {
		if err := run.append(app.SessionEvent{Type: "stream", Kind: "response", Content: fmt.Sprintf("event %d: ", i) + payload}); err != nil {
			t.Fatal(err)
		}
	}
	if err := run.append(app.SessionEvent{Type: "result", Outcome: "succeeded", Content: "done"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(run.logPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= 128<<20 {
		t.Fatal("fixture did not exceed the former total log limit")
	}
	// Read a middle page while the writer is still open.
	middle := mustStudioRunPage(t, run, 17, 1)
	if middle.NextCursor != 18 || middle.Events[0].Event.Content != "event 16: "+payload {
		t.Fatal("live middle page lost its cursor or content")
	}
	if err := run.close(); err != nil {
		t.Fatal(err)
	}
	for restart := range 2 {
		reloaded, err := loadLatestStudioRun(run.root, run.store)
		if err != nil {
			t.Fatalf("restart %d: %v", restart, err)
		}
		var after int64
		for after < 35 {
			page := mustStudioRunPage(t, reloaded, after, 3)
			if page.Run.Status != "completed" || page.Run.Cursor != 35 || len(page.Events) == 0 {
				t.Fatalf("restart %d after %d: status=%s cursor=%d count=%d", restart, after, page.Run.Status, page.Run.Cursor, len(page.Events))
			}
			for _, event := range page.Events {
				if event.Cursor != after+1 {
					t.Fatalf("restart %d: cursor %d after %d", restart, event.Cursor, after)
				}
				switch event.Cursor {
				case 1:
					if event.Event.Type != "session" {
						t.Fatal("lost initial session event")
					}
				case 35:
					if event.Event.Content != "done" {
						t.Fatal("lost final result")
					}
				default:
					if event.Event.Content != fmt.Sprintf("event %d: ", event.Cursor-2)+payload {
						t.Fatalf("restart %d: content changed at cursor %d", restart, event.Cursor)
					}
				}
				after = event.Cursor
			}
			if page.NextCursor != after {
				t.Fatal("page did not advance to its last event")
			}
		}
		if page := mustStudioRunPage(t, reloaded, after, 3); len(page.Events) != 0 || page.NextCursor != after {
			t.Fatal("replay duplicated events after the final cursor")
		}
	}
}

func TestStudioRunReplaysMaximumWritableEvent(t *testing.T) {
	for _, suffix := range []string{"\n", ""} {
		t.Run(fmt.Sprintf("newline=%v", suffix != ""), func(t *testing.T) {
			run := createPagingTestRun(t)
			if err := run.close(); err != nil {
				t.Fatal(err)
			}
			event := studioRunEvent{Cursor: 1, At: time.Date(2026, 11, 1, 0, 0, 0, 123456789, time.UTC), Event: app.SessionEvent{Type: "result", Outcome: "succeeded"}}
			body, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			event.Event.Content = strings.Repeat("x", maximumRunEventSize-len(body)-len(`,"content":""`))
			body, err = json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if len(body) != maximumRunEventSize {
				t.Fatalf("event size = %d", len(body))
			}
			if err := os.WriteFile(run.logPath(), append(body, suffix...), 0o600); err != nil {
				t.Fatal(err)
			}
			reloaded, err := loadLatestStudioRun(run.root, run.store)
			if err != nil {
				t.Fatal(err)
			}
			page := mustStudioRunPage(t, reloaded, 0, 1)
			if page.Run.Status != "completed" || len(page.Events) != 1 || page.Events[0].Event.Content != event.Event.Content {
				t.Fatal("a writable event could not be replayed intact")
			}
		})
	}
}

func TestStudioRunEventReadFailureReturnsHTTPError(t *testing.T) {
	run := createPagingTestRun(t)
	if err := run.append(app.SessionEvent{Type: "result", Outcome: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err := run.close(); err != nil {
		t.Fatal(err)
	}
	handler, commits, sessions, err := newHandlerRuntime(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sessions.Close(); err != nil {
			t.Error(err)
		}
		if err := commits.Close(); err != nil {
			t.Error(err)
		}
	})
	sessions.runs.runs[studioRunKey(run.root, run.store.SessionID, run.snapshot.ID)] = run
	if err := os.Truncate(run.logPath(), 0); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/sessions/%s/runs/%s/events?workspace_root=%s", run.store.SessionID, run.snapshot.ID, url.QueryEscape(run.root))
	response := serveJSON(t, handler, http.MethodGet, path, nil)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("missing indexed events = %d %s", response.Code, response.Body.String())
	}
}
