package gitwork

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/q/change"
)

func TestManagerCreatesSubmitsAndMergesChangeRequest(t *testing.T) {
	repository := newRepository(t)
	manager := Manager{Root: filepath.Join(t.TempDir(), "leases")}

	request, err := manager.Prepare(t.Context(), repository, "child-1")
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != change.StatusWorking || request.BaseRef != "main" || request.BaseCommit == "" {
		t.Fatalf("prepared request = %#v", request)
	}
	if branch := gitTest(t, repository, "branch", "--show-current"); branch != "main" {
		t.Fatalf("parent branch = %q", branch)
	}
	if branch := gitTest(t, request.WorktreePath, "branch", "--show-current"); branch != request.HeadRef {
		t.Fatalf("child branch = %q", branch)
	}
	if err := os.WriteFile(filepath.Join(request.WorktreePath, "child.txt"), []byte("delegated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	request, err = manager.Submit(t.Context(), request, "Add delegated file")
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != change.StatusOpen || request.HeadCommit == "" || request.HeadCommit == request.BaseCommit {
		t.Fatalf("submitted request = %#v", request)
	}
	if got := gitTest(t, repository, "rev-parse", request.HeadRef); got != request.HeadCommit {
		t.Fatalf("shared branch head = %q, want %q", got, request.HeadCommit)
	}
	diff, err := manager.Read(t.Context(), request)
	if err != nil || !strings.Contains(diff.Patch, "child.txt") {
		t.Fatalf("diff = %#v, err = %v", diff, err)
	}

	request, err = manager.Merge(t.Context(), repository, request)
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != change.StatusMerged || request.MergedCommit == "" || request.WorktreePath != "" {
		t.Fatalf("merged request = %#v", request)
	}
	if body, err := os.ReadFile(filepath.Join(repository, "child.txt")); err != nil || strings.ReplaceAll(string(body), "\r\n", "\n") != "delegated\n" {
		t.Fatalf("merged file = %q, err = %v", body, err)
	}
	if output, err := gitCommand(repository, "show-ref", "--verify", "--quiet", "refs/heads/"+request.HeadRef); err == nil {
		t.Fatalf("delegated branch still exists: %s", output)
	}
}

func TestManagerRejectsDirtyParent(t *testing.T) {
	repository := newRepository(t)
	if err := os.WriteFile(filepath.Join(repository, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := (Manager{Root: filepath.Join(t.TempDir(), "leases")}).Prepare(t.Context(), repository, "child")
	if err == nil || !strings.Contains(err.Error(), ErrParentDirty.Error()) {
		t.Fatalf("prepare dirty parent = %v", err)
	}
}

func TestManagerRecoversBranchCreatedBeforeWorktree(t *testing.T) {
	repository := newRepository(t)
	manager := Manager{Root: filepath.Join(t.TempDir(), "leases")}
	gitTest(t, repository, "branch", "q/delegate/recovered", "HEAD")

	request, err := manager.Prepare(t.Context(), repository, "recovered")
	if err != nil {
		t.Fatal(err)
	}
	if branch := gitTest(t, request.WorktreePath, "branch", "--show-current"); branch != request.HeadRef {
		t.Fatalf("recovered branch = %q, want %q", branch, request.HeadRef)
	}
}

func TestManagerCleanupCanResumeAfterWorktreeDisappears(t *testing.T) {
	repository := newRepository(t)
	manager := Manager{Root: filepath.Join(t.TempDir(), "leases")}
	request, err := manager.Prepare(t.Context(), repository, "cleanup")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(request.WorktreePath); err != nil {
		t.Fatal(err)
	}

	request, err = manager.Close(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != change.StatusClosed || request.WorktreePath != "" {
		t.Fatalf("closed request = %#v", request)
	}
	if _, err := gitCommand(repository, "show-ref", "--verify", "--quiet", "refs/heads/"+request.HeadRef); err == nil {
		t.Fatal("delegated branch still exists")
	}
}

func TestManagerAdoptsMergeCompletedBeforeStateSave(t *testing.T) {
	repository := newRepository(t)
	manager := Manager{Root: filepath.Join(t.TempDir(), "leases")}
	request, err := manager.Prepare(t.Context(), repository, "adopt-merge")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(request.WorktreePath, "adopted.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	request, err = manager.Submit(t.Context(), request, "Add adopted change")
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "-c", "user.name=Q", "-c", "user.email=q@localhost", "merge", "--no-ff", "-m", "simulated completed merge", request.HeadCommit)
	mergedCommit := gitTest(t, repository, "rev-parse", "HEAD")

	request, err = manager.Merge(t.Context(), repository, request)
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != change.StatusMerged || request.MergedCommit != mergedCommit || request.WorktreePath != "" {
		t.Fatalf("adopted request = %#v", request)
	}
}

func TestManagerRejectsRequestWhoseRepositoryDoesNotOwnLease(t *testing.T) {
	repository := newRepository(t)
	other := newRepository(t)
	manager := Manager{Root: filepath.Join(t.TempDir(), "leases")}
	request, err := manager.Prepare(t.Context(), repository, "tampered")
	if err != nil {
		t.Fatal(err)
	}
	request.RepositoryRoot = other

	if _, err := manager.Close(t.Context(), request); err == nil || !strings.Contains(err.Error(), "lease does not match") {
		t.Fatalf("close tampered request = %v", err)
	}
	if got := gitTest(t, repository, "rev-parse", request.HeadRef); got != request.BaseCommit {
		t.Fatalf("original branch changed: %q", got)
	}
}

func TestNestedChangeMergesIntoParentWorktree(t *testing.T) {
	repository := newRepository(t)
	manager := Manager{Root: filepath.Join(t.TempDir(), "leases")}
	outer, err := manager.Prepare(t.Context(), repository, "senior")
	if err != nil {
		t.Fatal(err)
	}
	inner, err := manager.Prepare(t.Context(), outer.WorktreePath, "junior")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner.WorktreePath, "nested.txt"), []byte("nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inner, err = manager.Submit(t.Context(), inner, "Add nested change")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Merge(t.Context(), outer.WorktreePath, inner); err != nil {
		t.Fatal(err)
	}
	outer, err = manager.Submit(t.Context(), outer, "Apply reviewed nested change")
	if err != nil {
		t.Fatal(err)
	}
	if outer.Status != change.StatusOpen || outer.HeadCommit == outer.BaseCommit {
		t.Fatalf("outer request = %#v", outer)
	}
	outer, err = manager.Merge(t.Context(), repository, outer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repository, "nested.txt")); err != nil {
		t.Fatal(err)
	}
}

func newRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitTest(t, root, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "add", "README.md")
	gitTest(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "initial")
	return root
}

func gitTest(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	body, err := gitCommand(root, arguments...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, body)
	}
	return strings.TrimSpace(body)
}

func gitCommand(root string, arguments ...string) (string, error) {
	command := exec.CommandContext(context.Background(), "git", append([]string{"-C", root}, arguments...)...)
	body, err := command.CombinedOutput()
	return string(body), err
}
