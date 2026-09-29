package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	if response.Code != http.StatusAccepted || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("run session = %d %s", response.Code, response.Body.String())
	}
	var started studioRunSnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil || started.ID == "" {
		t.Fatalf("run snapshot = %#v, %v", started, err)
	}
	var eventBody strings.Builder
	var cursor int64
	for range 10 {
		events := httptest.NewRecorder()
		handler.ServeHTTP(events, httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf("/api/v1/sessions/%s/runs/%s/events?workspace_root=%s&after=%d&wait_ms=5000", store.SessionID, started.ID, query, cursor),
			nil,
		))
		if events.Code != http.StatusOK {
			t.Fatalf("run events = %d %s", events.Code, events.Body.String())
		}
		var page studioRunPage
		if err := json.Unmarshal(events.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		eventBody.Write(events.Body.Bytes())
		cursor = page.NextCursor
		if terminalRunStatus(page.Run.Status) {
			break
		}
	}
	if !strings.Contains(eventBody.String(), `"type":"stream"`) || !strings.Contains(eventBody.String(), `"type":"result"`) {
		t.Fatalf("run events = %s", eventBody.String())
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

func TestRegisteredSessionsPersistTreesAndUnregisterWithoutDeletingSource(t *testing.T) {
	root := t.TempDir()
	store, lock, err := workspace.CreateSession(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.Save(workspace.Session{
		Title: "Registered root", UpdatedAt: &now,
		Transcript: []client.Message{{Role: client.RoleUser, Content: "root conversation"}},
	}); err != nil {
		t.Fatal(err)
	}
	bookmark := workspace.DelegationBookmark{
		InvocationID: "child-session", CallIndex: 0, ToolIndex: 0, CallID: "delegate-1",
		Agent: "builtin/research", Prompt: "inspect it", RunID: "child-run",
	}
	if _, err := store.AddDelegation(bookmark); err != nil {
		t.Fatal(err)
	}
	child, err := store.ChildStore(bookmark.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Save(workspace.Session{
		Title: "Research", UpdatedAt: &now,
		Transcript: []client.Message{{Role: client.RoleAssistant, Content: "child conversation"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	configStore := config.Store{Dir: t.TempDir()}
	handler, err := newHandler(configStore)
	if err != nil {
		t.Fatal(err)
	}
	register := serveJSON(t, handler, http.MethodPost, "/api/v1/registered-sessions", registerSessionRequest{
		WorkspaceRoot: root, SessionID: store.SessionID,
	})
	if register.Code != http.StatusCreated {
		t.Fatalf("register session = %d %s", register.Code, register.Body.String())
	}
	var registered registeredSessionTree
	if err := json.Unmarshal(register.Body.Bytes(), &registered); err != nil {
		t.Fatal(err)
	}
	if registered.RegistrationID == "" || registered.Session.Title != "Registered root" || len(registered.Delegations) != 1 ||
		len(registered.Delegations[0].Transcript) != 1 {
		t.Fatalf("registered tree = %#v", registered)
	}

	reopened, err := newHandler(configStore)
	if err != nil {
		t.Fatal(err)
	}
	list := httptest.NewRecorder()
	reopened.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/registered-sessions", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "child conversation") {
		t.Fatalf("persisted registry = %d %s", list.Code, list.Body.String())
	}

	unregister := httptest.NewRecorder()
	reopened.ServeHTTP(unregister, httptest.NewRequest(
		http.MethodDelete, "/api/v1/registered-sessions/"+registered.RegistrationID, nil,
	))
	if unregister.Code != http.StatusNoContent {
		t.Fatalf("unregister = %d %s", unregister.Code, unregister.Body.String())
	}
	if _, err := store.Load(); err != nil {
		t.Fatalf("unregister deleted source session: %v", err)
	}
	list = httptest.NewRecorder()
	reopened.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/registered-sessions", nil))
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "Registered root") {
		t.Fatalf("registry after unregister = %d %s", list.Code, list.Body.String())
	}
}

func TestRegisteredSessionsCanCreateAndRegisterRootSession(t *testing.T) {
	root := t.TempDir()
	handler, err := newHandler(config.Store{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	response := serveJSON(t, handler, http.MethodPost, "/api/v1/registered-sessions", registerSessionRequest{
		WorkspaceRoot: root,
		Create:        true,
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("create and register session = %d %s", response.Code, response.Body.String())
	}
	var registered registeredSessionTree
	if err := json.Unmarshal(response.Body.Bytes(), &registered); err != nil {
		t.Fatal(err)
	}
	if registered.RegistrationID == "" || registered.Session.SessionID == "" || registered.WorkspaceRoot != root {
		t.Fatalf("created registration = %#v", registered)
	}
	store, err := (workspace.Store{Root: root}).ForSession(registered.Session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatalf("load created root session: %v", err)
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

func TestSessionDelegationTreeIncludesChildStateAndTranscript(t *testing.T) {
	root := t.TempDir()
	store, lock, err := workspace.CreateSession(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	bookmark := workspace.DelegationBookmark{
		InvocationID: "child-one", CallIndex: 1, ToolIndex: 0, CallID: "delegate-1",
		Agent: "builtin/research", Prompt: "inspect the implementation", RunID: "child-run",
	}
	if _, err := store.AddDelegation(bookmark); err != nil {
		t.Fatal(err)
	}
	child, err := store.ChildStore(bookmark.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.SaveDelegationState(workspace.DelegationState{
		Agent: bookmark.Agent, Prompt: bookmark.Prompt, RunID: bookmark.RunID, Status: "running",
		RunningCall: &workspace.DelegationToolCall{CallID: "read-1", Name: "read_file"},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := child.Save(workspace.Session{RunID: bookmark.RunID, UpdatedAt: &now, Transcript: []client.Message{{Role: client.RoleAssistant, Content: "Inspecting files"}}}); err != nil {
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
		http.MethodGet, "/api/v1/sessions/"+store.SessionID+"/delegations?workspace_root="+url.QueryEscape(root), nil,
	))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"agent":"builtin/research"`) ||
		!strings.Contains(response.Body.String(), `"name":"read_file"`) || !strings.Contains(response.Body.String(), "Inspecting files") {
		t.Fatalf("delegation tree = %d %s", response.Code, response.Body.String())
	}
}

func TestSessionDelegationDeleteOnlyAcceptsCompletedChildren(t *testing.T) {
	root := t.TempDir()
	store, lock, err := workspace.CreateSession(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	for index, item := range []struct {
		id     string
		status string
	}{
		{id: "completed-child", status: "completed"},
		{id: "running-child", status: "running"},
	} {
		bookmark := workspace.DelegationBookmark{
			InvocationID: item.id, CallIndex: index, CallID: "call-" + item.id,
			Agent: "builtin/research", Prompt: "inspect", RunID: "run-" + item.id,
		}
		if _, err := store.AddDelegation(bookmark); err != nil {
			t.Fatal(err)
		}
		child, err := store.ChildStore(item.id)
		if err != nil {
			t.Fatal(err)
		}
		if err := child.Save(workspace.Session{RunID: bookmark.RunID}); err != nil {
			t.Fatal(err)
		}
		state := workspace.DelegationState{Agent: bookmark.Agent, Prompt: bookmark.Prompt, RunID: bookmark.RunID, Status: item.status}
		if item.status == "completed" {
			state.Result = &client.ToolResult{Content: "done"}
		}
		if err := child.SaveDelegationState(state); err != nil {
			t.Fatal(err)
		}
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(config.Store{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/sessions/" + store.SessionID + "/delegations?workspace_root=" + url.QueryEscape(root) + "&path="

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, base+"running-child", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("delete running delegation = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, base+"completed-child", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete completed delegation = %d %s", response.Code, response.Body.String())
	}
	remaining, err := store.LoadDelegations()
	if err != nil || len(remaining) != 1 || remaining[0].InvocationID != "running-child" {
		t.Fatalf("remaining delegations = %#v, %v", remaining, err)
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
