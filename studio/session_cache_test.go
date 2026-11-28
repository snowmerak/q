package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/workspace"
)

func TestSessionSummaryCacheInvalidatesOnUpdate(t *testing.T) {
	root := t.TempDir()
	store, lock, err := workspace.CreateSession(root, "tester")
	if err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()

	t0 := time.Now().UTC().Truncate(time.Second)
	if err := store.Save(workspace.Session{
		Version:   workspace.CurrentVersion,
		ID:        store.SessionID,
		Type:      workspace.Interaction,
		Title:     "Initial title",
		UpdatedAt: &t0,
	}); err != nil {
		t.Fatal(err)
	}

	cache := newSessionSummaryCache()
	first, err := cache.load(store)
	if err != nil || first.Title != "Initial title" {
		t.Fatalf("first load = %+v err=%v", first, err)
	}

	t1 := t0.Add(time.Second)
	if err := store.Save(workspace.Session{
		Version:   workspace.CurrentVersion,
		ID:        store.SessionID,
		Type:      workspace.Interaction,
		Title:     "Updated title longer",
		UpdatedAt: &t1,
		Transcript: []client.Message{
			{Role: client.RoleUser, Content: "ping"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	second, err := cache.load(store)
	if err != nil || second.Title != "Updated title longer" {
		t.Fatalf("second load = %+v err=%v", second, err)
	}
}

func TestRegisteredSessionsLimitAndIncludeDelegations(t *testing.T) {
	dir := t.TempDir()
	handler, err := newHandler(config.Store{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	baseTime := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	var latestID string
	for i := range 3 {
		store, lock, err := workspace.CreateSession(root, "Session")
		if err != nil {
			t.Fatal(err)
		}
		_ = lock.Close()
		ts := baseTime.Add(time.Duration(i) * time.Minute)
		if err := store.Save(workspace.Session{
			Version:   workspace.CurrentVersion,
			ID:        store.SessionID,
			Type:      workspace.Interaction,
			Title:     []string{"Oldest", "Middle", "Newest"}[i],
			UpdatedAt: &ts,
		}); err != nil {
			t.Fatal(err)
		}
		if i == 2 {
			child, err := store.ChildStore("child-1")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AddDelegation(workspace.DelegationBookmark{
				InvocationID: "child-1", Agent: "builtin/research", Prompt: "inspect", RunID: "run-child-1",
			}); err != nil {
				t.Fatal(err)
			}
			if err := child.SaveDelegationState(workspace.DelegationState{
				Agent: "builtin/research", Prompt: "inspect", RunID: "run-child-1", Status: "running",
			}); err != nil {
				t.Fatal(err)
			}
			latestID = store.SessionID
		}
		reg := serveJSON(t, handler, http.MethodPost, "/api/v1/registered-sessions", registerSessionRequest{
			WorkspaceRoot: root, SessionID: store.SessionID,
		})
		if reg.Code != http.StatusCreated {
			t.Fatalf("register %d = %d %s", i, reg.Code, reg.Body.String())
		}
	}

	// Overview fast query: omit delegations, return top 2 newest.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/registered-sessions?include_delegations=0&limit=2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET registered-sessions = %d %s", rec.Code, rec.Body.String())
	}
	var resp registeredSessionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Sessions) != 2 {
		t.Fatalf("len(resp.Sessions) = %d, want 2", len(resp.Sessions))
	}
	if resp.Sessions[0].Session.SessionID != latestID || resp.Sessions[0].Session.Title != "Newest" || len(resp.Sessions[0].Delegations) != 0 {
		t.Fatalf("unexpected first item: %+v", resp.Sessions[0])
	}
	if resp.Sessions[1].Session.Title != "Middle" {
		t.Fatalf("unexpected second item: %+v", resp.Sessions[1])
	}

	// Full query includes delegations on the newest item.
	recFull := httptest.NewRecorder()
	handler.ServeHTTP(recFull, httptest.NewRequest(http.MethodGet, "/api/v1/registered-sessions", nil))
	if recFull.Code != http.StatusOK {
		t.Fatalf("GET full registered-sessions = %d %s", recFull.Code, recFull.Body.String())
	}
	if err := json.Unmarshal(recFull.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Sessions) != 3 || len(resp.Sessions[0].Delegations) != 1 {
		t.Fatalf("unexpected full response: %+v", resp.Sessions)
	}
}

func TestEnsureProjectManagersIfMissingSkipsWarmAndRepairsDeleted(t *testing.T) {
	dir := studioCatalogTestDirectory(t, t.TempDir())
	service := newSessionsService(context.Background(), nil, dir)
	t.Cleanup(func() { _ = service.Close() })

	project, err := service.projects.create(workitems.WorkspaceInput{
		Name:           "mgr-fast-path",
		WorkspaceRoots: []string{t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	projects := []studioProject{project}

	items, err := service.registry.list()
	if err != nil {
		t.Fatal(err)
	}
	changed, err := service.ensureProjectManagersIfMissing(t.Context(), projects, items)
	if err != nil || !changed {
		t.Fatalf("first ensure = (%v, %v), want (true, nil)", changed, err)
	}
	items, err = service.registry.list()
	if err != nil || len(items) != 2 {
		t.Fatalf("items after first ensure = %d err=%v", len(items), err)
	}

	// Second call on warm registry skips work and reports changed=false.
	changed, err = service.ensureProjectManagersIfMissing(t.Context(), projects, items)
	if err != nil || changed {
		t.Fatalf("warm ensure = (%v, %v), want (false, nil)", changed, err)
	}

	// Removing one manager session file causes fast check to detect and repair it.
	if err := os.Remove(filepathSessionJSON(items[0].WorkspaceRoot, items[0].SessionID)); err != nil {
		t.Fatal(err)
	}
	changed, err = service.ensureProjectManagersIfMissing(t.Context(), projects, items)
	if err != nil || !changed {
		t.Fatalf("repair ensure = (%v, %v), want (true, nil)", changed, err)
	}
}

func filepathSessionJSON(root, id string) string {
	return (workspace.Store{Root: root, SessionID: id}).Path()
}
