package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
)

func TestRequiredManagerConcurrentCreationAndReset(t *testing.T) {
	root := filepath.Join(t.TempDir(), "manager")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			if _, err := EnsureManagerSession(ctx, root, "project", "manager"); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	store := Store{Root: root, SessionID: "manager"}
	entries, err := (Store{Root: root}).ListSessions()
	if err != nil || len(entries) != 1 || entries[0].Store.SessionID != store.SessionID {
		t.Fatalf("duplicate managers: %+v, %v", entries, err)
	}
	previous, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	previous.Transcript = []client.Message{{Role: client.RoleUser, Content: "preserve this conversation"}}
	if err := store.Save(previous); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessionModelConfig(SessionModelConfig{Agent: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.SessionDir(), ACPSessionFileName), []byte(`{"session_id":"remote"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireLock("running manager")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureManagerSession(ctx, root, "project", "manager"); err != nil {
		t.Fatalf("catalog refresh cannot inspect a running manager: %v", err)
	}
	if _, err := ResetSession(root, store.SessionID, "busy clear"); !errors.Is(err, ErrLocked) {
		t.Fatalf("clear overlapped a running manager: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	for _, remove := range []func() error{store.Clear, store.ClearSession, func() error { return DeleteSession(root, store.SessionID, "delete") }} {
		if err := remove(); !errors.Is(err, ErrRequiredManager) {
			t.Fatalf("manager deletion was allowed: %v", err)
		}
	}
	alias := Store{Root: root, SessionID: "MANAGER"}
	if canonicalInfo, err := os.Stat(store.SessionDir()); err == nil {
		if aliasInfo, err := os.Stat(alias.SessionDir()); err == nil && os.SameFile(canonicalInfo, aliasInfo) {
			for _, remove := range []func() error{alias.Clear, alias.ClearSession} {
				if err := remove(); !errors.Is(err, ErrRequiredManager) {
					t.Fatalf("case alias bypassed manager protection: %v", err)
				}
			}
		}
	}
	unchanged, err := store.Load()
	if err != nil || len(unchanged.Transcript) != 1 {
		t.Fatalf("refresh/delete changed the conversation: %+v, %v", unchanged, err)
	}
	reset, err := ResetSession(root, store.SessionID, "clear manager")
	if err != nil || reset.ID != previous.ID || reset.RunID == previous.RunID || len(reset.Transcript) != 0 {
		t.Fatalf("reset: %+v, %v", reset, err)
	}
	selection, err := store.LoadSessionModelConfig()
	if err != nil || selection.Agent != "codex" {
		t.Fatalf("clear lost provider selection: %+v, %v", selection, err)
	}
	if _, err := os.Stat(filepath.Join(store.SessionDir(), ACPSessionFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clear kept remote resume state: %v", err)
	}
	if err := store.RequireDeletable(); !errors.Is(err, ErrRequiredManager) {
		t.Fatalf("clear lost manager protection: %v", err)
	}
	if _, err := EnsureManagerSession(ctx, root, "different-project", "other-manager"); err == nil {
		t.Fatal("manager binding could be replaced")
	}
}

func TestRequiredManagerRepairsInterruptedCreation(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DirectoryName, "manager.json"), []byte(`{"workspace_id":"project","session_id":"manager"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := EnsureManagerSession(t.Context(), root, "project", "manager")
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Load()
	if err != nil || value.ID != "manager" {
		t.Fatalf("could not recover a binding saved before session creation: %+v, %v", value, err)
	}
	if err := os.WriteFile(store.Path(), []byte("corrupt existing history"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureManagerSession(t.Context(), root, "project", "manager"); err == nil {
		t.Fatal("corrupt history was silently replaced")
	}
}
