package lsp

import (
	"path/filepath"
	"testing"
)

func TestRouterSelectsConfiguredWorkspaceByPath(t *testing.T) {
	primaryRoot := t.TempDir()
	additionalRoot := t.TempDir()
	outsideRoot := t.TempDir()
	primary := &Manager{root: primaryRoot}
	additional := &Manager{root: additionalRoot}
	router := NewRouter(primary, additional)

	if selected, err := router.managerForPath("relative.go"); err != nil || selected != primary {
		t.Fatalf("relative path selected %p, err = %v", selected, err)
	}
	if selected, err := router.managerForPath(filepath.Join(additionalRoot, "nested", "file.go")); err != nil || selected != additional {
		t.Fatalf("additional path selected %p, err = %v", selected, err)
	}
	if _, err := router.managerForPath(filepath.Join(outsideRoot, "file.go")); err == nil {
		t.Fatal("router accepted a path outside configured workspaces")
	}
}
