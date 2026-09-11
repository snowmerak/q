package workspace

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/snowmerak/q/thinker"
)

func TestThinkerCheckpointRoundTripAndGuardedClear(t *testing.T) {
	store := Store{Root: t.TempDir(), SessionID: "11111111-1111-4111-8111-111111111111"}
	checkpoint := testThinkerCheckpoint("learn-one")
	if err := store.SaveThinkerCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := store.LoadThinkerCheckpoint()
	if err != nil || !found || loaded.JobID != checkpoint.JobID || loaded.Generation != checkpoint.Generation {
		t.Fatalf("loaded checkpoint = %#v, found=%v, err=%v", loaded, found, err)
	}
	if err := store.ClearThinkerCheckpoint("learn-two"); err == nil {
		t.Fatal("mismatched job cleared a Thinker checkpoint")
	}
	if _, found, err := store.LoadThinkerCheckpoint(); err != nil || !found {
		t.Fatalf("guarded checkpoint disappeared: found=%v err=%v", found, err)
	}
	if err := store.ClearThinkerCheckpoint(checkpoint.JobID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LoadThinkerCheckpoint(); err != nil || found {
		t.Fatalf("checkpoint after clear: found=%v err=%v", found, err)
	}
}

func TestThinkerCheckpointRejectsUnknownFields(t *testing.T) {
	store := Store{Root: t.TempDir(), SessionID: "11111111-1111-4111-8111-111111111111"}
	if err := os.MkdirAll(store.SessionDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ThinkerCheckpointPath(), []byte(`{"version":1,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LoadThinkerCheckpoint(); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown-field error = %v", err)
	}
	if err := store.ClearThinkerCheckpointAny(); err != nil {
		t.Fatal(err)
	}
}

func TestClearSessionRemovesThinkerCheckpoint(t *testing.T) {
	store := Store{Root: t.TempDir(), SessionID: "11111111-1111-4111-8111-111111111111"}
	if err := store.Save(Session{RunID: "run-one"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveThinkerCheckpoint(testThinkerCheckpoint("learn-one")); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearSession(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.SessionDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session directory after clear error = %v", err)
	}
}

func testThinkerCheckpoint(jobID string) thinker.JobCheckpoint {
	return thinker.JobCheckpoint{
		Version: thinker.JobCheckpointVersion,
		JobID:   jobID, InputDigest: strings.Repeat("ab", 32), Generation: strings.Repeat("cd", 12),
		Result: thinker.Result{},
	}
}
