package library

import (
	"errors"
	"net/http"
	"testing"

	"github.com/snowmerak/q/workitems"
)

func TestWorkspaceAPIOwnershipAndArchive(t *testing.T) {
	dir := t.TempDir()
	options := testOptions(dir, testConfig(t))
	leader, err := EnsureWithOptions(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := leader.Close(); err != nil {
			t.Error(err)
		}
	})
	follower, err := EnsureWithOptions(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = follower.Close() }()
	value, err := follower.Client().CreateWorkspace(t.Context(), workitems.WorkspaceInput{Name: "Project", WorkspaceRoots: []string{t.TempDir(), t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := leader.Client().ListWorkspaces(t.Context(), false)
	if err != nil || len(listed.Workspaces) != 1 || listed.Workspaces[0].ID != value.ID {
		t.Fatalf("shared catalog: %+v, %v", listed, err)
	}
	other := options
	other.Dir = t.TempDir()
	if runtime, err := EnsureWithOptions(t.Context(), other); err == nil {
		_ = runtime.Close()
		t.Fatal("different configuration directory joined the same Library")
	}
	if err := follower.Client().ArchiveWorkspace(t.Context(), value.ID); err != nil {
		t.Fatal(err)
	}
	listed, err = leader.Client().ListWorkspaces(t.Context(), false)
	if err != nil || len(listed.Workspaces) != 0 {
		t.Fatalf("active catalog: %+v, %v", listed, err)
	}
	listed, err = leader.Client().ListWorkspaces(t.Context(), true)
	if err != nil || len(listed.Workspaces) != 1 || listed.Workspaces[0].ManagerSessionID != value.ManagerSessionID {
		t.Fatalf("archive: %+v, %v", listed, err)
	}
	err = follower.Client().ArchiveWorkspace(t.Context(), "unknown")
	var response *ResponseError
	if !errors.As(err, &response) || response.StatusCode != http.StatusNotFound {
		t.Fatalf("typed not-found error: %v", err)
	}
}
