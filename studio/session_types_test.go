package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/council"
	"github.com/snowmerak/q/workspace"
)

type workflowGuardHost struct {
	councilTestHost
	released  int
	compacted int
}

func (h *workflowGuardHost) ReleaseSession(workspace.Store, string) error {
	h.released++
	return nil
}
func (h *workflowGuardHost) Compact(context.Context, workspace.Store, string) (string, error) {
	h.compacted++
	return "completed", nil
}

func TestTypedWorkflowsRejectOrdinarySessionMutations(t *testing.T) {
	host := &workflowGuardHost{}
	handler, commits, sessions, err := newHandlerRuntime(t.Context(), config.Store{Dir: studioCatalogTestDirectory(t, t.TempDir())}, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.Close(); _ = commits.Close() })
	idea := createTestComposer(t, handler)
	c, err := sessions.councils.store.Create(council.Council{
		Name: "Review", Scope: council.Independent,
		Members: []council.Seat{{Model: "test/one"}, {Model: "test/two"}}, Chair: council.Seat{Model: "test/chair"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := sessions.councils.store.SessionStore(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	response := serveJSON(t, handler, http.MethodGet, "/api/v1/registered-sessions", nil)
	var catalog registeredSessionsResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &catalog) != nil || len(catalog.Sessions) != 2 {
		t.Fatalf("typed catalog=%d %s", response.Code, response.Body.String())
	}
	seen := map[workspace.SessionType]bool{}
	for _, entry := range catalog.Sessions {
		seen[entry.Session.Type] = true
		if entry.RegistrationID == entry.Session.SessionID || len(entry.Delegations) != 0 {
			t.Fatalf("invalid typed root identity/tree: %+v", entry)
		}
	}
	if !seen[workspace.Council] || !seen[workspace.Composer] {
		t.Fatalf("catalog omitted workflows: %+v", catalog)
	}
	for _, store := range []workspace.Store{parent, sessions.composers.store.SessionStore(idea)} {
		before, err := os.ReadFile(store.Path())
		if err != nil {
			t.Fatal(err)
		}
		items, err := sessions.registry.list()
		if err != nil {
			t.Fatal(err)
		}
		registration := ""
		for _, item := range items {
			if item.SessionID == store.SessionID {
				registration = item.ID
			}
		}
		base := "/api/v1/sessions/" + store.SessionID
		for _, request := range []struct {
			method, path string
			body         any
		}{
			{http.MethodPost, base + "/messages", map[string]string{"workspace_root": store.Root, "content": "bypass workflow"}},
			{http.MethodPost, base + "/clear", map[string]string{"workspace_root": store.Root}},
			{http.MethodPost, base + "/compact", map[string]string{"workspace_root": store.Root}},
			{http.MethodPut, base + "/model", map[string]string{"workspace_root": store.Root, "model": "test/other"}},
			{http.MethodDelete, base + "?workspace_root=" + url.QueryEscape(store.Root), nil},
			{http.MethodDelete, "/api/v1/chats/" + registration, nil},
			{http.MethodDelete, "/api/v1/registered-sessions/" + registration, nil},
		} {
			response := serveJSON(t, handler, request.method, request.path, request.body)
			if response.Code != http.StatusConflict {
				t.Fatalf("%s %s = %d %s", request.method, request.path, response.Code, response.Body.String())
			}
		}
		after, err := os.ReadFile(store.Path())
		if err != nil || string(before) != string(after) {
			t.Fatal("ordinary endpoint changed a typed session", err)
		}
	}
	if host.released != 0 || host.compacted != 0 {
		t.Fatal("ordinary endpoint reached the typed runtime")
	}
}

func TestDeleteChatPreservesAttachedWorkflow(t *testing.T) {
	handler, commits, sessions, err := newHandlerRuntime(t.Context(), config.Store{Dir: t.TempDir()}, councilTestHost{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.Close(); _ = commits.Close() })
	chat, err := sessions.createChat()
	if err != nil {
		t.Fatal(err)
	}
	idea, err := sessions.composers.store.CreateInRoot(chat.WorkspaceRoot, "Attached idea", "test/one")
	if err != nil {
		t.Fatal(err)
	}
	response := serveJSON(t, handler, http.MethodDelete, "/api/v1/chats/"+chat.ID, nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete=%d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(chat.WorkspaceRoot, ".q", "sessions", idea.ID, "session.json")); err != nil {
		t.Fatal("deleting chat removed attached Composer", err)
	}
	if _, err := sessions.composers.store.Load(idea.ID); err != nil {
		t.Fatal(err)
	}
}
