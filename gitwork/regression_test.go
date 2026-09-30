package gitwork

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/snowmerak/q/change"
)

func TestSubmitWithoutChangesClosesAndRemovesLease(t *testing.T) {
	repository := newRepository(t)
	manager := Manager{Root: filepath.Join(t.TempDir(), "leases")}
	request, err := manager.Prepare(t.Context(), repository, "no-changes")
	if err != nil {
		t.Fatal(err)
	}
	path := request.WorktreePath
	request, err = manager.Submit(t.Context(), request, "Investigation only")
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != change.StatusClosed || request.WorktreePath != "" || request.HeadCommit != request.BaseCommit {
		t.Fatalf("empty request = %#v", request)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree not removed: %v", err)
	}
	if _, err := gitCommand(repository, "show-ref", "--verify", "--quiet", "refs/heads/"+request.HeadRef); err == nil {
		t.Fatal("empty task branch still exists")
	}
	if head := gitTest(t, repository, "rev-parse", "HEAD"); head != request.BaseCommit {
		t.Fatalf("parent changed: %s", head)
	}
}

func TestMergeRejectsChangedParentWithoutTouchingChild(t *testing.T) {
	for _, scenario := range []string{"dirty", "new-commit", "different-branch"} {
		t.Run(scenario, func(t *testing.T) {
			repository := newRepository(t)
			manager := Manager{Root: filepath.Join(t.TempDir(), "leases")}
			request, err := manager.Prepare(t.Context(), repository, "child")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(request.WorktreePath, "child.txt"), []byte("child"), 0o644); err != nil {
				t.Fatal(err)
			}
			request, err = manager.Submit(t.Context(), request, "Child change")
			if err != nil {
				t.Fatal(err)
			}
			wantError := ErrBaseMoved
			switch scenario {
			case "dirty":
				wantError = ErrParentDirty
				if err := os.WriteFile(filepath.Join(repository, "parent.txt"), []byte("dirty"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "new-commit":
				gitTest(t, repository, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "Parent advanced")
			case "different-branch":
				gitTest(t, repository, "switch", "-c", "other")
			}
			before := gitTest(t, repository, "rev-parse", "HEAD")
			result, err := manager.Merge(t.Context(), repository, request)
			if !errors.Is(err, wantError) || result.Status != change.StatusOpen {
				t.Fatalf("merge = %#v, %v; want %v", result, err, wantError)
			}
			if after := gitTest(t, repository, "rev-parse", "HEAD"); after != before {
				t.Fatalf("rejected merge changed parent: %s -> %s", before, after)
			}
			if _, err := os.Stat(filepath.Join(repository, "child.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("child leaked to parent: %v", err)
			}
			if head := gitTest(t, request.WorktreePath, "rev-parse", "HEAD"); head != request.HeadCommit {
				t.Fatalf("child changed: %s", head)
			}
			if _, err := manager.Close(t.Context(), request); err != nil {
				t.Fatal(err)
			}
		})
	}
}
