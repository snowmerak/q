package studio

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

func createTestChat(t *testing.T, handler http.Handler) registeredSessionTree {
	t.Helper()
	response := serveJSON(t, handler, http.MethodPost, "/api/v1/chats", struct{}{})
	if response.Code != http.StatusCreated {
		t.Fatalf("create chat = %d %s", response.Code, response.Body.String())
	}
	var chat registeredSessionTree
	if err := json.Unmarshal(response.Body.Bytes(), &chat); err != nil {
		t.Fatal(err)
	}
	return chat
}

func TestChatsPersistWithoutAWorkspaceAndDeleteOnlyTheirOwnData(t *testing.T) {
	settings := config.Store{Dir: t.TempDir()}
	handler, err := newHandler(settings)
	if err != nil {
		t.Fatal(err)
	}
	first, second := createTestChat(t, handler), createTestChat(t, handler)
	if first.Kind != "chat" || first.ProjectID != "" || first.WorkspaceRoot == second.WorkspaceRoot || filepath.Dir(first.WorkspaceRoot) != filepath.Join(settings.Dir, "chats") {
		t.Fatalf("chat roots are not isolated: %+v, %+v", first, second)
	}
	store := workspace.Store{Root: first.WorkspaceRoot, SessionID: first.Session.SessionID}
	value, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	value.Transcript = []client.Message{{Role: client.RoleUser, Content: "A personal conversation"}}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	reopened, err := newHandler(settings)
	if err != nil {
		t.Fatal(err)
	}
	list := serveJSON(t, reopened, http.MethodGet, "/api/v1/registered-sessions", nil)
	var registry registeredSessionsResponse
	if err := json.Unmarshal(list.Body.Bytes(), &registry); err != nil || list.Code != http.StatusOK || len(registry.Sessions) != 2 {
		t.Fatalf("reopened registry = %d %s, %v", list.Code, list.Body.String(), err)
	}
	for _, item := range registry.Sessions {
		if item.Kind != "chat" || item.ProjectID != "" {
			t.Fatalf("reopened chat attached to project: %+v", item)
		}
	}
	value, err = store.Load()
	if err != nil || len(value.Transcript) != 1 {
		t.Fatalf("chat history not retained: %+v, %v", value, err)
	}
	response := serveJSON(t, reopened, http.MethodDelete, "/api/v1/chats/"+first.RegistrationID, nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete chat = %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(first.WorkspaceRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted chat directory remains: %v", err)
	}
	if _, err := (workspace.Store{Root: second.WorkspaceRoot, SessionID: second.Session.SessionID}).Load(); err != nil {
		t.Fatalf("deleting chat affected another chat: %v", err)
	}
	// A registration for a user workspace must never qualify for recursive deletion.
	root := t.TempDir()
	registered := serveJSON(t, reopened, http.MethodPost, "/api/v1/registered-sessions", registerSessionRequest{WorkspaceRoot: root, Create: true})
	var external registeredSessionTree
	if err := json.Unmarshal(registered.Body.Bytes(), &external); err != nil {
		t.Fatal(err)
	}
	response = serveJSON(t, reopened, http.MethodDelete, "/api/v1/chats/"+external.RegistrationID, nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("external workspace accepted for deletion: %d %s", response.Code, response.Body.String())
	}
	if _, err := (workspace.Store{Root: root, SessionID: external.Session.SessionID}).Load(); err != nil {
		t.Fatalf("external workspace changed: %v", err)
	}
}

func TestStudioACPChatRunsAndDeletesThroughTheRealRuntime(t *testing.T) {
	fixture := newStudioIntegration(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	configured := serveJSON(t, fixture.handler, http.MethodPut, "/api/v1/settings/acp", map[string]any{"connections": map[string]config.AgentConnectionConfig{
		"delete-fixture": {Command: executable, Env: map[string]string{"Q_STUDIO_ACP_FIXTURE": "1"}},
	}})
	if configured.Code != http.StatusOK {
		t.Fatal(configured.Body.String())
	}
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	chat := createTestChat(t, fixture.handler)
	chatFixture := fixture
	chatFixture.root = chat.WorkspaceRoot
	chatFixture.session = workspace.Store{Root: chat.WorkspaceRoot, SessionID: chat.Session.SessionID}
	selected := serveJSON(t, fixture.handler, http.MethodPut, "/api/v1/sessions/"+chat.Session.SessionID+"/model", map[string]string{"workspace_root": chat.WorkspaceRoot, "agent": "delete-fixture"})
	if selected.Code != http.StatusOK {
		t.Fatal(selected.Body.String())
	}
	run := chatFixture.start(t, "hello ACP")
	endpoint := server.URL + "/api/v1/sessions/" + chat.Session.SessionID + "/runs/" + run.ID + "/events?workspace_root=" + url.QueryEscape(chat.WorkspaceRoot)
	page := waitStudioPage(t, endpoint, func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
	if page.Run.Status != "completed" {
		t.Fatalf("ACP turn = %+v", page.Run)
	}
	if _, err := os.Stat(filepath.Join(chatFixture.session.SessionDir(), workspace.ACPSessionFileName)); err != nil {
		t.Fatalf("ACP checkpoint missing: %v", err)
	}
	response := serveJSON(t, fixture.handler, http.MethodDelete, "/api/v1/chats/"+chat.RegistrationID, nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete ACP chat = %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(chat.WorkspaceRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ACP chat directory remains: %v", err)
	}
}

func TestStudioChatRetriesDeletionWithOnlyACPCheckpointRemaining(t *testing.T) {
	handler, err := newHandler(config.Store{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	chat := createTestChat(t, handler)
	store := workspace.Store{Root: chat.WorkspaceRoot, SessionID: chat.Session.SessionID}
	if err := os.WriteFile(filepath.Join(store.SessionDir(), workspace.ACPSessionFileName), []byte(`{"session_id":"previous-session"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.Path()); err != nil {
		t.Fatal(err)
	}
	response := serveJSON(t, handler, http.MethodDelete, "/api/v1/chats/"+chat.RegistrationID, nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("retry delete = %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(chat.WorkspaceRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partially deleted ACP chat remains: %v", err)
	}
}

func TestStudioChatRunsAndDeletesThroughTheRealRuntime(t *testing.T) {
	fixture := newStudioIntegration(t)
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	chat := createTestChat(t, fixture.handler)
	chatFixture := fixture
	chatFixture.root = chat.WorkspaceRoot
	chatFixture.session = workspace.Store{Root: chat.WorkspaceRoot, SessionID: chat.Session.SessionID}
	run := chatFixture.start(t, "write the requested file")
	endpoint := server.URL + "/api/v1/sessions/" + chat.Session.SessionID + "/runs/" + run.ID + "/events?workspace_root=" + url.QueryEscape(chat.WorkspaceRoot)
	page := waitStudioPage(t, endpoint, func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
	if page.Run.Status != "completed" {
		t.Fatalf("chat turn = %+v", page.Run)
	}
	if _, err := os.Stat(filepath.Join(chat.WorkspaceRoot, "studio-result.txt")); err != nil {
		t.Fatalf("chat did not use its private root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "studio-result.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("chat affected an existing workspace: %v", err)
	}
	response := serveJSON(t, fixture.handler, http.MethodDelete, "/api/v1/chats/"+chat.RegistrationID, nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete live chat = %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(chat.WorkspaceRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("chat directory still present: %v", err)
	}
}
