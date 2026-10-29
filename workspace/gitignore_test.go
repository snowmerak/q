package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLocalQExclusionPreservesProjectFilesAndTrackedChanges(t *testing.T) {
	root := newGitRepository(t)
	run := func(args ...string) string {
		t.Helper()
		output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return string(output)
	}
	if err := os.MkdirAll(filepath.Join(root, DirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(root, DirectoryName, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", ".q/tracked.txt")
	run("-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "base")
	excludePath := filepath.Join(root, ".git", "info", "exclude")
	if err := os.WriteFile(excludePath, []byte("# keep existing rules\n*.local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := (Store{Root: root}).EnsureQGitLocallyIgnored(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(excludePath)
	if err != nil || string(body) != "# keep existing rules\n*.local\n.q\n" {
		t.Fatalf("exclude rules changed: %q %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(root, GitIgnoreFileName)); !os.IsNotExist(err) {
		t.Fatalf("local exclusion edited project .gitignore: %v", err)
	}
	for path, content := range map[string]string{tracked: "modified", filepath.Join(root, DirectoryName, "runtime.json"): "metadata", filepath.Join(root, "user.txt"): "user work"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	status := run("status", "--porcelain=v1", "--untracked-files=all")
	if !strings.Contains(status, " M .q/tracked.txt") || !strings.Contains(status, "?? user.txt") || strings.Contains(status, "runtime.json") {
		t.Fatalf("exclusion hid user work or exposed metadata: %s", status)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	run("worktree", "add", "--detach", linked, "HEAD")
	if err := (Store{Root: linked}).EnsureQGitLocallyIgnored(t.Context()); err != nil {
		t.Fatalf("local exclusion failed with a worktree .git file: %v", err)
	}
	body, err = os.ReadFile(excludePath)
	if err != nil || strings.Count(string(body), ".q\n") != 1 {
		t.Fatalf("linked worktree did not reuse common exclude: %q %v", body, err)
	}
}

func TestEnsureQGitIgnoredCreatesRepositoryRootGitIgnore(t *testing.T) {
	repositoryRoot := newGitRepository(t)
	workspaceRoot := filepath.Join(repositoryRoot, "nested", "workspace")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repositoryRoot, ".git", "info", "exclude"), []byte(".q\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := (Store{Root: workspaceRoot}).EnsureQGitIgnored(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := (Store{Root: workspaceRoot}).EnsureQGitIgnored(context.Background()); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(repositoryRoot, GitIgnoreFileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != ".q\n" {
		t.Fatalf(".gitignore = %q", body)
	}
}

func TestEnsureQGitIgnoredKeepsExistingMatchingRule(t *testing.T) {
	for _, rule := range []string{".q\n", ".q/\n", "**/.q/\n"} {
		t.Run(rule, func(t *testing.T) {
			repositoryRoot := newGitRepository(t)
			path := filepath.Join(repositoryRoot, GitIgnoreFileName)
			if err := os.WriteFile(path, []byte(rule), 0o640); err != nil {
				t.Fatal(err)
			}

			if err := (Store{Root: repositoryRoot}).EnsureQGitIgnored(context.Background()); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != rule {
				t.Fatalf(".gitignore = %q, want %q", body, rule)
			}
		})
	}
}

func TestEnsureQGitIgnoredAppendsWithExistingLineEnding(t *testing.T) {
	for name, initial := range map[string]string{
		"lf without final newline": "bin/",
		"crlf with final newline":  "bin/\r\n",
		"empty existing gitignore": "",
	} {
		t.Run(name, func(t *testing.T) {
			repositoryRoot := newGitRepository(t)
			path := filepath.Join(repositoryRoot, GitIgnoreFileName)
			if err := os.WriteFile(path, []byte(initial), 0o640); err != nil {
				t.Fatal(err)
			}

			if err := (Store{Root: repositoryRoot}).EnsureQGitIgnored(context.Background()); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			lineEnding := "\n"
			if initial == "bin/\r\n" {
				lineEnding = "\r\n"
			}
			want := initial
			if want != "" && want[len(want)-1] != '\n' {
				want += lineEnding
			}
			want += ".q" + lineEnding
			if string(body) != want {
				t.Fatalf(".gitignore = %q, want %q", body, want)
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0o640 {
					t.Fatalf(".gitignore permissions = %o", info.Mode().Perm())
				}
			}
		})
	}
}

func TestEnsureQGitIgnoredDoesNothingOutsideGitRepository(t *testing.T) {
	root := t.TempDir()
	if err := (Store{Root: root}).EnsureQGitIgnored(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, GitIgnoreFileName)); !os.IsNotExist(err) {
		t.Fatalf(".gitignore stat error = %v", err)
	}
}

func newGitRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	command := exec.Command("git", "-C", root, "init", "--quiet")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	return root
}
