package agentskills

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallAndUpdateGitSkill(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	repository := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init", "-b", "main")
	runGit(t, repository, "config", "user.email", "skills@example.test")
	runGit(t, repository, "config", "user.name", "Skills Test")
	writeGitSkill(t, repository, "First Git description.")
	runGit(t, repository, "add", "SKILL.md")
	runGit(t, repository, "commit", "-m", "initial")

	registry, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := registry.InstallGit(context.Background(), "global", repository)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Name != "git-managed-skill" || installed.Scope != "global" {
		t.Fatalf("installed=%#v", installed)
	}

	writeGitSkill(t, repository, "Second Git description.")
	runGit(t, repository, "add", "SKILL.md")
	runGit(t, repository, "commit", "-m", "update")
	updated, err := registry.UpdateGit(context.Background(), installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Description != "Second Git description." {
		t.Fatalf("updated=%#v", updated)
	}
	removed, err := registry.RemoveGit(updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if removed.Name != "git-managed-skill" {
		t.Fatalf("removed=%#v", removed)
	}
	if _, err := os.Stat(removed.Directory); !os.IsNotExist(err) {
		t.Fatalf("removed directory still exists: %v", err)
	}
}

func writeGitSkill(t *testing.T, directory, description string) {
	t.Helper()
	body := "---\nname: git-managed-skill\ndescription: " + description + "\n---\n\n# Git managed\n"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
}
