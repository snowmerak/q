package studio

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

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
	if len(catalog.Projects) != 1 || len(catalog.Sessions) != 1 || catalog.Sessions[0].ProjectID != project.ID || catalog.Sessions[0].ProjectName != "Q" {
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
