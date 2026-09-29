package gitwork

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryRootDistinguishesPlainDirectoryAndGitWorkTree(t *testing.T) {
	plain := t.TempDir()
	if root, found, err := RepositoryRoot(t.Context(), plain); err != nil || found || root != "" {
		t.Fatalf("plain directory = root %q, found %v, err %v", root, found, err)
	}
	repository := t.TempDir()
	command := exec.CommandContext(t.Context(), "git", "-C", repository, "init", "-b", "main")
	if body, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, body)
	}
	subdirectory := filepath.Join(repository, "nested")
	if err := os.Mkdir(subdirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	root, found, err := RepositoryRoot(t.Context(), subdirectory)
	if err != nil || !found || !strings.EqualFold(filepath.Clean(root), filepath.Clean(repository)) {
		t.Fatalf("Git directory = root %q, found %v, err %v", root, found, err)
	}
}
