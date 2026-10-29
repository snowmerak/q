package studio

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/workspace"
)

type embeddingCatalogRunner struct {
	settingsRuntimeRunner
	roots func() ([]string, error)
}

func TestStudioProjectCatalogMigratesAndSharesLibraryAuthority(t *testing.T) {
	dir := studioCatalogTestDirectory(t, t.TempDir())
	root, auxiliary := t.TempDir(), t.TempDir()
	session, lock, err := workspace.CreateSession(root, "migration test")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	id, err := workspace.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"version": 1, "projects": []map[string]any{{"id": id, "name": "Original", "workspace_roots": []string{root, auxiliary}, "created_at": "2026-10-29T08:33:00+09:00", "updated_at": "2026-10-29T08:33:00+09:00"}}})
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "studio-projects.json")
	if err := os.WriteFile(legacy, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newSessionRegistry(dir).register(root, session.SessionID); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(config.Store{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	response := serveJSON(t, handler, http.MethodGet, "/api/v1/registered-sessions", nil)
	var catalog registeredSessionsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(catalog.Projects) != 1 || len(catalog.Sessions) != 3 || catalog.Projects[0].ID != id {
		t.Fatalf("migration changed identities: %d %+v", response.Code, catalog)
	}
	var preserved bool
	for _, item := range catalog.Sessions {
		preserved = preserved || item.ProjectID == id && item.Session.SessionID == session.SessionID
	}
	if !preserved {
		t.Fatal("migration detached the existing session")
	}
	library, err := qlibrary.Ensure(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = library.Close() }()
	if _, err := library.Client().UpdateWorkspace(t.Context(), id, workitems.WorkspaceInput{Name: "Central", WorkspaceRoots: []string{root, auxiliary}}); err != nil {
		t.Fatal(err)
	}
	response = serveJSON(t, handler, http.MethodGet, "/api/v1/projects", nil)
	var projects studioProjectsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &projects); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(projects.Projects) != 1 || projects.Projects[0].Name != "Central" {
		t.Fatalf("Studio did not use Library authority: %d %s", response.Code, response.Body.String())
	}
	backup, err := os.ReadFile(legacy)
	if err != nil || !bytes.Equal(backup, body) {
		t.Fatalf("legacy backup was overwritten: %v", err)
	}
}

func (runner *embeddingCatalogRunner) SetEmbeddingWorkspaceResolver(resolver func() ([]string, error)) {
	runner.roots = resolver
}

func TestEmbeddingCatalogIncludesProjectAndIndependentSessionRoots(t *testing.T) {
	dir := studioCatalogTestDirectory(t, t.TempDir())
	projectRoot, sessionRoot := t.TempDir(), t.TempDir()
	if _, err := newStudioProjectStore(dir).create(studioProjectUpdateRequest{Name: "project", WorkspaceRoots: []string{projectRoot}}); err != nil {
		t.Fatal(err)
	}
	store, lock, err := workspace.CreateSession(sessionRoot, "independent")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := newSessionRegistry(dir).register(sessionRoot, store.SessionID); err != nil {
		t.Fatal(err)
	}
	runner := &embeddingCatalogRunner{}
	service := newSessionsService(t.Context(), runner, dir)
	defer func() { _ = service.Close() }()
	if runner.roots == nil {
		t.Fatal("embedding workspace catalog was not wired to the runtime")
	}
	roots, err := runner.roots()
	if err != nil || !reflect.DeepEqual(roots, []string{projectRoot, sessionRoot}) {
		t.Fatalf("embedding workspace catalog = %v, %v", roots, err)
	}
}

func TestStudioProjectsGroupSessionsAndResolveAuxiliaryRoots(t *testing.T) {
	configDirectory := t.TempDir()
	primary := t.TempDir()
	auxiliary := t.TempDir()
	store, lock, err := workspace.CreateSession(primary, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(config.Store{Dir: configDirectory})
	if err != nil {
		t.Fatal(err)
	}

	registered := serveJSON(t, handler, http.MethodPost, "/api/v1/registered-sessions", registerSessionRequest{
		WorkspaceRoot: primary, SessionID: store.SessionID,
	})
	if registered.Code != http.StatusCreated {
		t.Fatalf("register session = %d %s", registered.Code, registered.Body.String())
	}
	created := serveJSON(t, handler, http.MethodPost, "/api/v1/projects", studioProjectUpdateRequest{
		Name: "Q", WorkspaceRoots: []string{primary, auxiliary},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create project = %d %s", created.Code, created.Body.String())
	}
	var project studioProject
	if err := json.Unmarshal(created.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}

	response := serveJSON(t, handler, http.MethodGet, "/api/v1/registered-sessions", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list sessions = %d %s", response.Code, response.Body.String())
	}
	var catalog registeredSessionsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Projects) != 1 || len(catalog.Sessions) != 3 || catalog.Sessions[0].ProjectID != project.ID || catalog.Sessions[0].ProjectName != "Q" {
		t.Fatalf("project session catalog = %#v", catalog)
	}

	projects := newStudioProjectStore(configDirectory)
	resolved, found, err := projects.resolve(primary)
	if err != nil || !found {
		t.Fatalf("resolve project = %#v, %v, %v", resolved, found, err)
	}
	if resolved.ProjectName != "Q" || len(resolved.AuxiliaryRoots) != 1 || resolved.AuxiliaryRoots[0] != auxiliary {
		t.Fatalf("resolved project = %#v", resolved)
	}

	replacement := t.TempDir()
	updated := serveJSON(t, handler, http.MethodPut, "/api/v1/projects/"+url.PathEscape(project.ID), studioProjectUpdateRequest{
		Name: "Q workspace", WorkspaceRoots: []string{primary, replacement},
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("update project = %d %s", updated.Code, updated.Body.String())
	}
	resolved, found, err = projects.resolve(primary)
	if err != nil || !found || resolved.ProjectName != "Q workspace" || len(resolved.AuxiliaryRoots) != 1 || resolved.AuxiliaryRoots[0] != replacement {
		t.Fatalf("resolve updated project = %#v, %v, %v", resolved, found, err)
	}

	deleted := serveJSON(t, handler, http.MethodDelete, "/api/v1/projects/"+url.PathEscape(project.ID), nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete project = %d %s", deleted.Code, deleted.Body.String())
	}
	response = serveJSON(t, handler, http.MethodGet, "/api/v1/registered-sessions", nil)
	catalog = registeredSessionsResponse{}
	if err := json.Unmarshal(response.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Projects) != 0 || len(catalog.Sessions) != 1 || catalog.Sessions[0].ProjectID != "" {
		t.Fatalf("independent session catalog = %#v", catalog)
	}
}

func TestStudioProjectsRejectWorkspaceSharedByTwoProjects(t *testing.T) {
	handler, err := newHandler(config.Store{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	first := serveJSON(t, handler, http.MethodPost, "/api/v1/projects", studioProjectUpdateRequest{
		Name: "First", WorkspaceRoots: []string{root},
	})
	if first.Code != http.StatusCreated {
		t.Fatalf("create first project = %d %s", first.Code, first.Body.String())
	}
	second := serveJSON(t, handler, http.MethodPost, "/api/v1/projects", studioProjectUpdateRequest{
		Name: "Second", WorkspaceRoots: []string{root},
	})
	if second.Code != http.StatusBadRequest {
		t.Fatalf("shared workspace = %d %s", second.Code, second.Body.String())
	}
}
