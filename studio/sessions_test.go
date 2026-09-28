package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

type sessionRunnerFunc func(context.Context, workspace.Store, string, string, app.SessionEventSink) error

func (function sessionRunnerFunc) Run(
	ctx context.Context,
	store workspace.Store,
	sessionID string,
	prompt string,
	emit app.SessionEventSink,
) error {
	return function(ctx, store, sessionID, prompt, emit)
}

func TestSessionsAPIListsLoadsCreatesAndStreams(t *testing.T) {
	root := t.TempDir()
	store, lock, err := workspace.CreateSession(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.Save(workspace.Session{
		ID: store.SessionID, RunID: "run-test", Title: "Existing session", UpdatedAt: &now,
		Transcript: []client.Message{{Role: client.RoleUser, Content: "hello"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	runner := sessionRunnerFunc(func(
		_ context.Context, runStore workspace.Store, sessionID, prompt string, emit app.SessionEventSink,
	) error {
		if runStore.Root != root || sessionID != store.SessionID || prompt != "continue" {
			t.Fatalf("run = %#v %q %q", runStore, sessionID, prompt)
		}
		for _, event := range []app.SessionEvent{
			{Type: "session", SessionID: sessionID, WorkingDirectory: root},
			{Type: "stream", Kind: "response", Content: "done"},
			{Type: "result", SessionID: sessionID, Outcome: "succeeded", Content: "done"},
		} {
			if err := emit(event); err != nil {
				return err
			}
		}
		return nil
	})
	handler, err := newHandlerWithRunner(config.Store{Dir: t.TempDir()}, runner)
	if err != nil {
		t.Fatal(err)
	}
	query := url.QueryEscape(root)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/sessions?workspace_root="+query, nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Existing session") {
		t.Fatalf("list sessions = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+store.SessionID+"?workspace_root="+query, nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"content":"hello"`) {
		t.Fatalf("load session = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/sessions", bytes.NewBufferString(`{"workspace_root":`+quotedJSON(root)+`}`),
	))
	if response.Code != http.StatusCreated {
		t.Fatalf("create session = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/sessions/"+store.SessionID+"/messages",
		bytes.NewBufferString(`{"workspace_root":`+quotedJSON(root)+`,"content":"continue"}`),
	))
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("run session = %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"type":"stream"`) || !strings.Contains(response.Body.String(), `"type":"result"`) {
		t.Fatalf("stream = %s", response.Body.String())
	}
}

func TestSessionsAPIRejectsMissingWorkspace(t *testing.T) {
	handler, err := newHandler(config.Store{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "workspace_root") {
		t.Fatalf("missing workspace = %d %s", response.Code, response.Body.String())
	}
}

func TestSessionsAPIClearsAndDeletesSessions(t *testing.T) {
	root := t.TempDir()
	store, lock, err := workspace.CreateSession(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	originalRunID := "run-before-clear"
	now := time.Now().UTC()
	if err := store.Save(workspace.Session{
		RunID: originalRunID, Title: "Clear me", UpdatedAt: &now,
		Transcript: []client.Message{{Role: client.RoleUser, Content: "old"}},
		Context:    []client.Message{{Role: client.RoleUser, Content: "old"}},
		ActiveTask: &workspace.ActiveTask{Objective: "old task", StartedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	handler, err := newHandler(config.Store{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/sessions/"+store.SessionID+"/clear",
		bytes.NewBufferString(`{"workspace_root":`+quotedJSON(root)+`}`),
	))
	if response.Code != http.StatusOK {
		t.Fatalf("clear session = %d %s", response.Code, response.Body.String())
	}
	var cleared sessionDetail
	if err := json.Unmarshal(response.Body.Bytes(), &cleared); err != nil {
		t.Fatal(err)
	}
	if len(cleared.Transcript) != 0 || cleared.ActiveTask != nil || cleared.Session.Title != "" {
		t.Fatalf("cleared session = %#v", cleared)
	}
	if cleared.Session.RunID == "" || cleared.Session.RunID == originalRunID {
		t.Fatalf("cleared run ID = %q", cleared.Session.RunID)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete,
		"/api/v1/sessions/"+store.SessionID+"?workspace_root="+url.QueryEscape(root),
		nil,
	))
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete session = %d %s", response.Code, response.Body.String())
	}
	if _, err := store.Load(); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("load deleted session = %v", err)
	}
}

func TestWorkspaceLearningAPI(t *testing.T) {
	root := t.TempDir()
	handler, err := newHandler(config.Store{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/workspaces/learning?workspace_root="+url.QueryEscape(root), nil,
	))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"enabled":true`) {
		t.Fatalf("default learning = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPut, "/api/v1/workspaces/learning",
		bytes.NewBufferString(`{"workspace_root":`+quotedJSON(root)+`,"disabled":true}`),
	))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"enabled":false`) {
		t.Fatalf("disable learning = %d %s", response.Code, response.Body.String())
	}
	value, err := (workspace.Store{Root: root}).LoadLearningConfig()
	if err != nil || !value.Disabled {
		t.Fatalf("stored learning = %#v, %v", value, err)
	}
}

func TestDirectoryBrowserDefaultsToHomeAndListsDirectories(t *testing.T) {
	home := t.TempDir()
	child := filepath.Join(home, "repository")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "ignored.txt"), []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}

	listing, err := listDirectories("", home)
	if err != nil {
		t.Fatal(err)
	}
	if listing.Current != home || listing.Home != home {
		t.Fatalf("listing paths = current %q home %q", listing.Current, listing.Home)
	}
	if listing.CanSelect {
		t.Fatal("home directory must not be selectable as a workspace")
	}
	if len(listing.Directories) != 1 || listing.Directories[0].Name != "repository" || listing.Directories[0].Path != child {
		t.Fatalf("directories = %#v", listing.Directories)
	}
}

func quotedJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
