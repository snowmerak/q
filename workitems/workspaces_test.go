package workitems

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func openTestStore(t *testing.T, dir string) *Store {
	t.Helper()
	store, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func TestWorkspaceCatalogAtomicUpdatesAndArchive(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	root, auxiliary, other := t.TempDir(), t.TempDir(), t.TempDir()
	created, err := store.CreateWorkspace(t.Context(), WorkspaceInput{Name: "Project", WorkspaceRoots: []string{root, auxiliary}})
	if err != nil {
		t.Fatal(err)
	}
	if uuid.Validate(created.ID) != nil || uuid.Validate(created.ManagerSessionID) != nil || uuid.Validate(created.CoordinatorSessionID) != nil || created.ID == created.ManagerSessionID || created.CoordinatorSessionID == created.ManagerSessionID {
		t.Fatalf("identities: %+v", created)
	}
	second, err := store.CreateWorkspace(t.Context(), WorkspaceInput{Name: "Second", WorkspaceRoots: []string{other}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateWorkspace(t.Context(), created.ID, WorkspaceInput{Name: "Changed", WorkspaceRoots: []string{other}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting update: %v", err)
	}
	values, err := store.ListWorkspaces(t.Context(), false)
	if err != nil || len(values) != 2 || !reflect.DeepEqual(values[0], created) {
		t.Fatalf("failed update was not atomic: %+v, %v", values, err)
	}
	updated, err := store.UpdateWorkspace(t.Context(), created.ID, WorkspaceInput{Name: "Renamed", WorkspaceRoots: []string{auxiliary, root}})
	if err != nil || updated.ID != created.ID || updated.ManagerSessionID != created.ManagerSessionID || updated.CoordinatorSessionID != created.CoordinatorSessionID || !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("updated: %+v, %v", updated, err)
	}
	if err := store.ArchiveWorkspace(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	active, err := store.ListWorkspaces(t.Context(), false)
	if err != nil || len(active) != 1 || active[0].ID != second.ID {
		t.Fatalf("active workspaces: %+v, %v", active, err)
	}
	all, err := store.ListWorkspaces(t.Context(), true)
	if err != nil || len(all) != 2 || all[0].ArchivedAt == nil || all[0].ManagerSessionID != created.ManagerSessionID {
		t.Fatalf("archive discarded identity: %+v, %v", all, err)
	}
	if _, err := store.CreateWorkspace(t.Context(), WorkspaceInput{Name: "Renamed", WorkspaceRoots: []string{root}}); err != nil {
		t.Fatalf("archived root was not released: %v", err)
	}
}

func TestWorkspaceCatalogConcurrentClientsCannotShareActiveRoot(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	root := t.TempDir()
	var group sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		group.Go(func() {
			_, err := store.CreateWorkspace(t.Context(), WorkspaceInput{Name: uuid.NewString(), WorkspaceRoots: []string{root}})
			results <- err
		})
	}
	group.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("created %d workspaces for one directory", succeeded)
	}
}

func TestWorkspaceVersionFiveAddsCoordinatorWithoutReplacingIntegrator(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.CreateWorkspace(t.Context(), WorkspaceInput{Name: "Existing", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := store.CreateItem(t.Context(), CreateInput{WorkspaceID: before.ID, Kind: Issue, Title: "Keep knowledge", Content: "Existing evidence", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	// Reconstruct the previous schema; every other table and source row remains.
	if _, err := store.db.ExecContext(t.Context(), `DROP INDEX workspace_coordinator_session; ALTER TABLE workspaces DROP COLUMN coordinator_session_id; PRAGMA user_version=5`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := store.ListWorkspaces(t.Context(), false)
	if err != nil || len(projects) != 1 {
		t.Fatalf("migrated projects: %+v, %v", projects, err)
	}
	after := projects[0]
	if after.ManagerSessionID != before.ManagerSessionID || after.ID != before.ID || uuid.Validate(after.CoordinatorSessionID) != nil || after.CoordinatorSessionID == after.ManagerSessionID || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("migration replaced existing identity: %+v -> %+v", before, after)
	}
	if saved, err := store.GetItem(t.Context(), issue.ID); err != nil || saved.BodyPostID != issue.BodyPostID {
		t.Fatalf("migration lost knowledge: %+v, %v", saved, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openTestStore(t, dir)
	projects, err = store.ListWorkspaces(t.Context(), false)
	if err != nil || len(projects) != 1 || projects[0].CoordinatorSessionID != after.CoordinatorSessionID {
		t.Fatalf("reopen changed coordinator: %+v, %v", projects, err)
	}
}

func TestWorkspaceCatalogImportsLegacyOnceAndPreservesSource(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	id := uuid.NewString()
	now := time.Date(2026, 10, 29, 8, 33, 0, 0, time.FixedZone("KST", 9*60*60))
	input := struct {
		Version  int              `json:"version"`
		Projects []map[string]any `json:"projects"`
	}{1, []map[string]any{{"id": id, "name": "Legacy", "workspace_roots": []string{root}, "created_at": now, "updated_at": now}}}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "studio-projects.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	values, err := store.ListWorkspaces(t.Context(), false)
	if err != nil || len(values) != 1 || values[0].ID != id || !values[0].CreatedAt.Equal(now) {
		t.Fatalf("migration: %+v, %v", values, err)
	}
	manager := values[0].ManagerSessionID
	if _, err := store.UpdateWorkspace(t.Context(), id, WorkspaceInput{Name: "Central", WorkspaceRoots: []string{root}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(backup, body) {
		t.Fatalf("legacy source was modified: %v", err)
	}
	store = openTestStore(t, dir)
	values, err = store.ListWorkspaces(t.Context(), false)
	if err != nil || len(values) != 1 || values[0].Name != "Central" || values[0].ManagerSessionID != manager {
		t.Fatalf("reopening replayed obsolete JSON: %+v, %v", values, err)
	}
}

func TestWorkspaceMigrationFailureRollsBackAllProjects(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	item := func(name string) map[string]any {
		return map[string]any{"id": uuid.NewString(), "name": name, "workspace_roots": []string{root}, "created_at": time.Now().UTC(), "updated_at": time.Now().UTC()}
	}
	projects := []map[string]any{item("One"), item("Two")}
	write := func(projects []map[string]any) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"version": 1, "projects": projects})
		if err := os.WriteFile(filepath.Join(dir, "studio-projects.json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(projects)
	if store, err := Open(t.Context(), dir); err == nil {
		_ = store.Close()
		t.Fatal("conflicting legacy catalog was accepted")
	}
	write(projects[:1])
	store := openTestStore(t, dir)
	values, err := store.ListWorkspaces(t.Context(), false)
	if err != nil || len(values) != 1 || values[0].ID != projects[0]["id"] {
		t.Fatalf("migration retry did not recover atomically: %+v, %v", values, err)
	}
}
