package coordinator_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/snowmerak/q/coordinator"
	"github.com/snowmerak/q/internal/sessioncatalog"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/workspace"
)

func TestManagerCatalogCannotUnregisterAndRootsDoNotMoveHistory(t *testing.T) {
	dir := t.TempDir()
	project := workitems.Workspace{ID: uuid.NewString(), ManagerSessionID: uuid.NewString(), Name: "Q", WorkspaceRoots: []string{t.TempDir(), t.TempDir()}}
	store, err := coordinator.EnsureManager(t.Context(), dir, project)
	if err != nil {
		t.Fatal(err)
	}
	if store.Root != filepath.Join(dir, "workspaces", project.ID) {
		t.Fatalf("manager root: %s", store.Root)
	}
	catalog := sessioncatalog.New(dir)
	entry, err := catalog.Register(store.Root, store.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Unregister(entry.ID); !errors.Is(err, workspace.ErrRequiredManager) {
		t.Fatalf("catalog detached required manager: %v", err)
	}
	project.WorkspaceRoots = []string{project.WorkspaceRoots[1]}
	again, err := coordinator.EnsureManager(t.Context(), dir, project)
	if err != nil || again != store {
		t.Fatalf("root changes moved manager history: %+v, %v", again, err)
	}
	entries, err := catalog.List()
	if err != nil || len(entries) != 1 || entries[0].ID != entry.ID {
		t.Fatalf("registration changed: %+v, %v", entries, err)
	}
}
