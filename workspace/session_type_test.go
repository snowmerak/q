package workspace

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTypedSessionsPreserveTypeAndSelectOnlyInteraction(t *testing.T) {
	root := t.TempDir()
	normal, lock, err := CreateSession(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()
	for _, kind := range []SessionType{Council, Composer} {
		store, lock, err := CreateTypedSession(root, "test", kind)
		if err != nil {
			t.Fatal(err)
		}
		_ = lock.Close()
		if err := store.Save(Session{Title: "saved without repeating the type"}); err != nil {
			t.Fatal(err)
		}
		value, err := store.Load()
		if err != nil || value.Type != kind {
			t.Fatalf("save lost type: %+v %v", value, err)
		}
		body, err := os.ReadFile(store.Path())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"type": "`+string(kind)+`"`) {
			t.Fatalf("missing explicit type: %s", body)
		}
		value, err = ResetSession(root, store.SessionID, "test reset")
		if err != nil || value.Type != kind {
			t.Fatalf("reset lost type: %+v %v", value, err)
		}
		if err := DeleteSession(root, store.SessionID, "wrong endpoint"); err == nil {
			t.Fatal("ordinary delete accepted typed session")
		}
	}
	selected, lock, err := OpenLatestSession(root, "test latest")
	if err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()
	if selected.SessionID != normal.SessionID {
		t.Fatal("ordinary latest session selected a typed workflow")
	}
	entries, err := (Store{Root: root}).ListSessions()
	if err != nil || len(entries) != 3 {
		t.Fatalf("typed list=%+v %v", entries, err)
	}
	var legacy Session
	if err := json.Unmarshal([]byte(`{"version":2,"learning":{}}`), &legacy); err != nil || legacy.Type != Interaction {
		t.Fatalf("old interaction=%+v %v", legacy, err)
	}
	if err := json.Unmarshal([]byte(`{"version":3,"type":"unknown","learning":{}}`), &legacy); err == nil {
		t.Fatal("unknown workflow accepted")
	}
}

func TestCouncilParticipantOwnsNestedContextAndLock(t *testing.T) {
	parent, lock, err := CreateTypedSession(t.TempDir(), "test", Council)
	if err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()
	childID, err := NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.CouncilParticipant(childID)
	if err != nil {
		t.Fatal(err)
	}
	if child.SessionDir() != filepath.Join(parent.SessionDir(), "council", "participants", childID) {
		t.Fatalf("unexpected participant address: %s", child.SessionDir())
	}
	if _, err := child.ForSession("different"); err == nil {
		t.Fatal("internal session escaped its address")
	}
	selected, err := child.ForSession(childID)
	if err != nil || selected.SessionDir() != child.SessionDir() {
		t.Fatalf("nested address lost: %+v %v", selected, err)
	}
	guard, err := child.AcquireLock("test child")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := child.AcquireLock("competing child"); !errors.Is(err, ErrLocked) {
		t.Fatalf("nested lock=%v", err)
	}
	if err := child.Save(Session{Title: "participant"}); err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := (Store{Root: parent.Root}).ListSessions()
	if err != nil || len(entries) != 1 {
		t.Fatalf("participant leaked into root list: %+v %v", entries, err)
	}
	if _, err := parent.CouncilParticipant("../../escape"); err == nil {
		t.Fatal("participant path escaped")
	}
	if err := parent.RemoveTypedSessionDirectory(Composer); err == nil {
		t.Fatal("wrong workflow deletion accepted")
	}
	if err := parent.RemoveTypedSessionDirectory(Council); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(parent.Root); err != nil {
		t.Fatal("workspace removed with session")
	}
}
