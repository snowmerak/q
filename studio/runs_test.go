package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

func TestStudioRunSurvivesRequestAndReplaysFromDisk(t *testing.T) {
	root := t.TempDir()
	store, lock, err := workspace.CreateSession(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	cancelled := make(chan struct{}, 1)
	runner := sessionRunnerFunc(func(ctx context.Context, _ workspace.Store, sessionID, prompt string, emit app.SessionEventSink) error {
		if prompt != "keep working" {
			t.Errorf("prompt = %q", prompt)
		}
		if err := emit(app.SessionEvent{Type: "session", SessionID: sessionID, WorkingDirectory: root}); err != nil {
			return err
		}
		if err := emit(app.SessionEvent{Type: "context_usage", ContextUsed: 32000, ContextSize: 128000}); err != nil {
			return err
		}
		close(started)
		select {
		case <-release:
			if err := emit(app.SessionEvent{Type: "stream", Kind: "response", Content: "durable"}); err != nil {
				return err
			}
			return emit(app.SessionEvent{Type: "result", SessionID: sessionID, Outcome: "succeeded", Content: "durable"})
		case <-ctx.Done():
			cancelled <- struct{}{}
			return ctx.Err()
		}
	})
	handler, commits, sessions, err := newHandlerRuntime(t.Context(), config.Store{Dir: t.TempDir()}, runner)
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

	requestContext, cancelRequest := context.WithCancel(context.Background())
	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/sessions/"+store.SessionID+"/messages",
		bytes.NewBufferString(`{"workspace_root":`+quotedJSON(root)+`,"content":"keep working"}`),
	).WithContext(requestContext)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	cancelRequest()
	if response.Code != http.StatusAccepted {
		t.Fatalf("start run = %d %s", response.Code, response.Body.String())
	}
	var run studioRunSnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("background run did not start")
	}
	select {
	case <-cancelled:
		t.Fatal("browser request cancellation stopped the background run")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)

	query := url.QueryEscape(root)
	var page studioRunPage
	for range 20 {
		events := httptest.NewRecorder()
		handler.ServeHTTP(events, httptest.NewRequest(
			http.MethodGet,
			"/api/v1/sessions/"+store.SessionID+"/runs/"+run.ID+"/events?workspace_root="+query+"&after=0&wait_ms=1000",
			nil,
		))
		if events.Code != http.StatusOK {
			t.Fatalf("events = %d %s", events.Code, events.Body.String())
		}
		if err := json.Unmarshal(events.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if terminalRunStatus(page.Run.Status) {
			break
		}
	}
	if page.Run.Status != "completed" || page.Run.ContextUsed != 32000 || page.Run.ContextSize != 128000 || len(page.Events) < 4 {
		t.Fatalf("completed page = %#v", page)
	}
	empty := httptest.NewRecorder()
	handler.ServeHTTP(empty, httptest.NewRequest(
		http.MethodGet,
		fmt.Sprintf("/api/v1/sessions/%s/runs/%s/events?workspace_root=%s&after=%d", store.SessionID, run.ID, query, page.NextCursor),
		nil,
	))
	var replayTail studioRunPage
	if empty.Code != http.StatusOK || json.Unmarshal(empty.Body.Bytes(), &replayTail) != nil || len(replayTail.Events) != 0 {
		t.Fatalf("cursor replay tail = %d %s", empty.Code, empty.Body.String())
	}
	late := serveJSON(t, handler, http.MethodPost, "/api/v1/sessions/"+store.SessionID+"/runs/"+run.ID+"/commands", studioRunCommandRequest{
		WorkspaceRoot: root, Action: "cancel",
	})
	if late.Code != http.StatusConflict {
		t.Fatalf("late command = %d %s", late.Code, late.Body.String())
	}

	if err := sessions.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := newSessionRunService(t.Context(), nil)
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	replayed, err := reopened.latest(root, store.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	loaded := replayed.page(0, maximumRunEventPage)
	encoded, _ := json.Marshal(loaded.Events)
	if loaded.Run.Status != "completed" || loaded.Run.ContextUsed != 32000 || loaded.Run.ContextSize != 128000 || !strings.Contains(string(encoded), `"content":"durable"`) {
		t.Fatalf("replayed run = %#v events=%s", loaded.Run, encoded)
	}
}

func TestStudioRunRemainsActiveUntilQueuedGuidanceRedirects(t *testing.T) {
	now := time.Now().UTC()
	for _, terminal := range []app.SessionEvent{
		{Type: "result", Outcome: "succeeded"},
		{Type: "cancelled"},
		{Type: "error", Detail: "interrupted by guidance"},
	} {
		run := &studioRun{
			guidance: "continue with the correction",
			snapshot: studioRunSnapshot{Status: "running", FinishedAt: timePointer(now.Add(-time.Second))},
		}
		run.applyEventLocked(terminal, now)
		if run.snapshot.Status != "redirecting" || run.snapshot.FinishedAt != nil {
			t.Fatalf("%s while guidance queued = status %q, finished_at %v", terminal.Type, run.snapshot.Status, run.snapshot.FinishedAt)
		}
		run.applyEventLocked(app.SessionEvent{Type: "redirect"}, now.Add(time.Second))
		if run.snapshot.Status != "redirected" || run.snapshot.FinishedAt == nil {
			t.Fatalf("%s after redirect = status %q, finished_at %v", terminal.Type, run.snapshot.Status, run.snapshot.FinishedAt)
		}
	}
}

func TestStudioRunsAdmitOneTurnPerSessionAndRunSessionsConcurrently(t *testing.T) {
	root := t.TempDir()
	first, firstLock, err := workspace.CreateSession(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := firstLock.Close(); err != nil {
		t.Fatal(err)
	}
	second, secondLock, err := workspace.CreateSession(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := secondLock.Close(); err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 2)
	release := make(chan struct{})
	runner := sessionRunnerFunc(func(ctx context.Context, _ workspace.Store, sessionID, _ string, emit app.SessionEventSink) error {
		if err := emit(app.SessionEvent{Type: "session", SessionID: sessionID}); err != nil {
			return err
		}
		started <- sessionID
		select {
		case <-release:
			return emit(app.SessionEvent{Type: "result", SessionID: sessionID, Outcome: "succeeded"})
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	service := newSessionRunService(t.Context(), runner)
	defer func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := service.start(root, first.SessionID, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.start(root, second.SessionID, "second"); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for range 2 {
		select {
		case sessionID := <-started:
			seen[sessionID] = true
		case <-time.After(5 * time.Second):
			t.Fatal("parallel sessions did not start")
		}
	}
	if !seen[first.SessionID] || !seen[second.SessionID] {
		t.Fatalf("started sessions = %#v", seen)
	}
	if _, err := service.start(root, first.SessionID, "duplicate"); err == nil || !strings.Contains(err.Error(), "already has a running turn") {
		t.Fatalf("same-session admission error = %v", err)
	}
	close(release)
}
