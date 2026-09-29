package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestNestedDelegationStoreAndBookmarks(t *testing.T) {
	root := Store{Root: t.TempDir(), SessionID: "root-session"}
	first, err := root.ChildStore("first-child")
	if err != nil {
		t.Fatal(err)
	}
	second, err := first.ChildStore("second-child")
	if err != nil {
		t.Fatal(err)
	}
	if second.SessionDir() != filepath.Join(root.SessionDir(), "delegates", "first-child", "delegates", "second-child") {
		t.Fatal(second.SessionDir())
	}
	item := DelegationBookmark{InvocationID: "first-child", CallIndex: 2, ToolIndex: 1, CallID: "same-id", Agent: "workspace/a", Prompt: "do", RunID: "run"}
	if _, err := root.AddDelegation(item); err != nil {
		t.Fatal(err)
	}
	if _, err := root.AddDelegation(item); err != nil {
		t.Fatal(err)
	}
	items, err := root.LoadDelegations()
	if err != nil || len(items) != 1 {
		t.Fatalf("bookmarks=%#v err=%v", items, err)
	}
	conflicting := item
	conflicting.Prompt = "different"
	if _, err := root.AddDelegation(conflicting); err == nil {
		t.Fatal("conflicting bookmark accepted")
	}
	if _, err := root.ChildStore("../escape"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := root.ChildStore("nested/escape"); err == nil {
		t.Fatal("nested path accepted")
	}
	if err := second.Save(Session{RunID: "run", Transcript: []client.Message{{Role: client.RoleUser, Content: "child"}}}); err != nil {
		t.Fatal(err)
	}
	listed, err := root.ListSessions()
	if err != nil || len(listed) != 0 {
		t.Fatalf("nested child leaked into session list: %#v, %v", listed, err)
	}
}

func TestDelegationStateRejectsMalformedVersionAndTrailingJSON(t *testing.T) {
	store := Store{Root: t.TempDir()}
	child, err := store.ChildStore("child")
	if err != nil {
		t.Fatal(err)
	}
	if err := child.SaveDelegationState(DelegationState{Agent: "workspace/a", Prompt: "do", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	state, err := child.LoadDelegationState()
	if err != nil || state.Version != delegationVersion {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	for _, body := range []string{`{"version":999,"agent":"a","prompt":"do","status":"running"}`, `{"version":1,"agent":"a","prompt":"do","status":"running"} {}`, `{"version":1,"agent":"a","prompt":"do","status":"invalid"}`} {
		if err := os.WriteFile(child.DelegationStatePath(), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := child.LoadDelegationState(); err == nil {
			t.Fatalf("accepted malformed state %q", body)
		}
	}
}

func TestDelegationLoadRejectsSymlinkFileAndDuplicateChildID(t *testing.T) {
	root := Store{Root: t.TempDir(), SessionID: "root-session"}
	if err := root.Save(Session{RunID: "run"}); err != nil {
		t.Fatal(err)
	}
	duplicate := `{"version":1,"items":[{"invocation_id":"same","call_index":1,"tool_index":0,"agent":"a","prompt":"one"},{"invocation_id":"same","call_index":2,"tool_index":0,"agent":"a","prompt":"two"}]}`
	if err := os.WriteFile(root.DelegationsPath(), []byte(duplicate), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := root.LoadDelegations(); err == nil {
		t.Fatal("duplicate child ID accepted")
	}
	if err := os.Remove(root.DelegationsPath()); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"version":1,"items":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root.DelegationsPath()); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := root.LoadDelegations(); err == nil {
		t.Fatal("symlink bookmark file accepted")
	}
	if _, err := root.AddDelegation(DelegationBookmark{InvocationID: "child", Agent: "a", Prompt: "do"}); err == nil {
		t.Fatal("symlink bookmark file overwritten")
	}
}

func TestDeleteSessionRemovesDelegationTreeButRejectsSymlink(t *testing.T) {
	root := Store{Root: t.TempDir(), SessionID: "root-session"}
	child, err := root.ChildStore("child")
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Save(Session{RunID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err := child.Save(Session{RunID: "run"}); err != nil {
		t.Fatal(err)
	}
	if _, err := root.AddDelegation(DelegationBookmark{InvocationID: "child", Agent: "workspace/a", Prompt: "do"}); err != nil {
		t.Fatal(err)
	}
	if err := root.ClearSession(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root.SessionDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session directory still exists: %v", err)
	}

	other := Store{Root: t.TempDir(), SessionID: "root-session"}
	if err := other.Save(Session{RunID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(other.SessionDir(), "delegates"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	link := filepath.Join(other.SessionDir(), "delegates", "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := other.ClearSession(); err == nil {
		t.Fatal("symlink tree deleted")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside target changed: %v", err)
	}
}

func TestDeleteCompletedDelegationRemovesOnlyCompletedChildTree(t *testing.T) {
	root := t.TempDir()
	store, lock, err := CreateSession(root, "test setup")
	if err != nil {
		t.Fatal(err)
	}
	for index, item := range []struct {
		id     string
		status string
	}{
		{id: "completed-child", status: "completed"},
		{id: "running-child", status: "running"},
	} {
		bookmark := DelegationBookmark{
			InvocationID: item.id, CallIndex: index, CallID: "call-" + item.id,
			Agent: "builtin/research", Prompt: "inspect", RunID: "run-" + item.id,
		}
		if _, err := store.AddDelegation(bookmark); err != nil {
			t.Fatal(err)
		}
		child, err := store.ChildStore(item.id)
		if err != nil {
			t.Fatal(err)
		}
		if err := child.Save(Session{RunID: bookmark.RunID}); err != nil {
			t.Fatal(err)
		}
		state := DelegationState{Agent: bookmark.Agent, Prompt: bookmark.Prompt, RunID: bookmark.RunID, Status: item.status}
		if item.status == "completed" {
			state.Result = &client.ToolResult{Content: "done"}
		}
		if err := child.SaveDelegationState(state); err != nil {
			t.Fatal(err)
		}
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	if err := DeleteCompletedDelegation(root, store.SessionID, []string{"completed-child"}, "test delete"); err != nil {
		t.Fatal(err)
	}
	bookmarks, err := store.LoadDelegations()
	if err != nil || len(bookmarks) != 1 || bookmarks[0].InvocationID != "running-child" {
		t.Fatalf("remaining bookmarks = %#v, %v", bookmarks, err)
	}
	completed, err := store.ChildStore("completed-child")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := completed.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("load deleted delegation = %v", err)
	}
	if err := DeleteCompletedDelegation(root, store.SessionID, []string{"running-child"}, "test reject"); !errors.Is(err, ErrDelegationNotCompleted) {
		t.Fatalf("delete running delegation = %v", err)
	}
	running, err := store.ChildStore("running-child")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := running.Load(); err != nil {
		t.Fatalf("running delegation was changed: %v", err)
	}
	nestedBookmark := DelegationBookmark{
		InvocationID: "nested-completed", CallIndex: 0, CallID: "call-nested",
		Agent: "builtin/junior-developer", Prompt: "implement", RunID: "run-nested",
	}
	if _, err := running.AddDelegation(nestedBookmark); err != nil {
		t.Fatal(err)
	}
	nested, err := running.ChildStore(nestedBookmark.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := nested.Save(Session{RunID: nestedBookmark.RunID}); err != nil {
		t.Fatal(err)
	}
	if err := nested.SaveDelegationState(DelegationState{
		Agent: nestedBookmark.Agent, Prompt: nestedBookmark.Prompt, RunID: nestedBookmark.RunID,
		Status: "completed", Result: &client.ToolResult{Content: "implemented"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteCompletedDelegation(root, store.SessionID, []string{"running-child", "nested-completed"}, "test nested delete"); err != nil {
		t.Fatal(err)
	}
	if remaining, err := running.LoadDelegations(); err != nil || len(remaining) != 0 {
		t.Fatalf("nested bookmarks = %#v, %v", remaining, err)
	}
	if _, err := running.Load(); err != nil {
		t.Fatalf("parent delegation was changed: %v", err)
	}
}

func TestClearSessionRejectsInvalidIDBeforeDeletingAnything(t *testing.T) {
	root := t.TempDir()
	sentinel := filepath.Join(root, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := Store{Root: root, SessionID: "../outside"}
	if err := store.ClearSession(); err == nil {
		t.Fatal("invalid session ID accepted")
	}
	if body, err := os.ReadFile(sentinel); err != nil || string(body) != "keep" {
		t.Fatalf("sentinel=%q err=%v", body, err)
	}
}
