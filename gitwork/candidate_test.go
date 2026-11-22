package gitwork

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/snowmerak/q/change"
)

func TestCandidateMergeReplansAndRecoversAfterTargetAdvances(t *testing.T) {
	ctx := t.Context()
	root := newRepository(t)
	manager := Manager{Root: filepath.Join(t.TempDir(), "leases")}
	prepare := func(id, name, body string) change.Request {
		t.Helper()
		req, err := manager.Prepare(ctx, root, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(req.WorktreePath, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		req, err = manager.Submit(ctx, req, id)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	candidate := func(req change.Request) change.MergeCandidate {
		t.Helper()
		out, err := manager.PrepareCandidate(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	a := prepare("first", "README.md", "first\n")
	b := prepare("second", "b.txt", "second\n")
	conflicting := prepare("third", "README.md", "conflicting\n")
	stale := candidate(b)
	if _, err := manager.ApplyCandidate(ctx, a, candidate(a)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ApplyCandidate(ctx, b, stale); !errors.Is(err, ErrBaseMoved) {
		t.Fatalf("stale proposal: %v", err)
	}
	current := candidate(b)
	if _, err := manager.ApplyCandidate(ctx, b, current); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PrepareCandidate(ctx, conflicting); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if err := Clean(ctx, root); err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "later")
	adopted, err := manager.ApplyCandidate(ctx, b, current)
	if err != nil || adopted.MergedCommit != current.Commit {
		t.Fatalf("recovery: %+v %v", adopted, err)
	}
}

func TestCandidateMergePreservesCommitHooks(t *testing.T) {
	root := newRepository(t)
	ctx := t.Context()
	manager := Manager{Root: filepath.Join(t.TempDir(), "leases")}
	request, err := manager.Prepare(ctx, root, "hooked")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(request.WorktreePath, "new.txt"), []byte("reviewed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request, err = manager.Submit(ctx, request, "Hooked merge")
	if err != nil {
		t.Fatal(err)
	}
	hooks := t.TempDir()
	gitTest(t, root, "config", "core.hooksPath", hooks)
	if err := os.WriteFile(filepath.Join(hooks, "pre-merge-commit"), []byte("#!/bin/sh\nexit 42\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PrepareCandidate(ctx, request); !errors.Is(err, ErrMergeCommitHooks) {
		t.Fatalf("candidate bypassed commit hook: %v", err)
	}
}
