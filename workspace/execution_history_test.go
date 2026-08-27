package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/subagent"
)

func completedExecutionCheckpoint() subagent.ExecutionCheckpoint {
	checkpoint := testExecutionCheckpoint()
	checkpoint.Phase = subagent.ExecutionPhaseCompleted
	checkpoint.TaskIndex, checkpoint.CompletedTasks, checkpoint.Attempts = 1, 1, 1
	checkpoint.Tasks = []subagent.TaskExecutionResult{{
		TaskIndex: 0, Title: checkpoint.Plan.Steps[0].Title, Attempts: 1,
		Result: subagent.CoderResult{Outcome: "succeeded", Summary: "Updated files", Verification: []string{"Tests passed"}},
	}}
	return checkpoint
}

func TestArchiveExecutionPreservesSnapshotAndUsesUniqueUTCTimestamps(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, time.August, 28, 12, 34, 56, 123456789, time.FixedZone("KST", 9*60*60))
	archived := make(map[string][]byte)
	for _, sessionID := range []string{"session-a", "session-a", "session-b"} {
		store := Store{Root: root, SessionID: sessionID}
		checkpoint := completedExecutionCheckpoint()
		checkpoint.RunID = "run-" + sessionID
		if err := store.SaveExecution(checkpoint); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(store.ExecutionPath())
		if err != nil {
			t.Fatal(err)
		}
		path, err := store.archiveExecutionAt(now)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(path) != store.ExecutionHistoryDir() ||
			!strings.HasPrefix(filepath.Base(path), "plan-execution-20260828T033456.123456789Z-") ||
			!strings.HasSuffix(path, ".json") || archived[path] != nil {
			t.Fatalf("invalid or reused history path: %s", path)
		}
		archived[path] = before
		if _, err := store.LoadExecution(); !errors.Is(err, ErrExecutionNotFound) {
			t.Fatalf("completed execution is still resumable: %v", err)
		}
		if retryPath, err := store.ArchiveExecution(); err != nil || retryPath != "" {
			t.Fatalf("retry archived a missing checkpoint: path=%q err=%v", retryPath, err)
		}
	}
	entries, err := os.ReadDir((Store{Root: root}).ExecutionHistoryDir())
	if err != nil || len(entries) != 3 {
		t.Fatalf("history entries=%v, err=%v", entries, err)
	}
	for path, want := range archived {
		body, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(body, want) {
			t.Fatalf("archived snapshot changed: path=%s body=%s err=%v", path, body, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("history permissions are too broad: %o", info.Mode().Perm())
		}
	}
}

func TestArchiveExecutionKeepsCheckpointWhenHistoryIsUnavailable(t *testing.T) {
	store := Store{Root: t.TempDir()}
	checkpoint := completedExecutionCheckpoint()
	if err := store.SaveExecution(checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ExecutionHistoryDir(), []byte("block history directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if path, err := store.ArchiveExecution(); err == nil || path != "" {
		t.Fatalf("archive unexpectedly succeeded: path=%q, err=%v", path, err)
	}
	if got, err := store.LoadExecution(); err != nil || !reflect.DeepEqual(got, checkpoint) {
		t.Fatalf("archive failure lost checkpoint: got=%#v, err=%v", got, err)
	}
	if err := os.Remove(store.ExecutionHistoryDir()); err != nil {
		t.Fatal(err)
	}
	if path, err := store.ArchiveExecution(); err != nil || path == "" {
		t.Fatalf("archive retry failed: path=%q, err=%v", path, err)
	}
}

func TestArchiveExecutionRejectsUnfinishedPlansAndIgnoresMissingCheckpoint(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if path, err := store.ArchiveExecution(); err != nil || path != "" {
		t.Fatalf("missing checkpoint: path=%q, err=%v", path, err)
	}
	checkpoint := testExecutionCheckpoint()
	if err := store.SaveExecution(checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ArchiveExecution(); err == nil || !strings.Contains(err.Error(), "only completed") {
		t.Fatalf("archived unfinished plan: %v", err)
	}
	if got, err := store.LoadExecution(); err != nil || !reflect.DeepEqual(got, checkpoint) {
		t.Fatalf("unfinished checkpoint changed: got=%#v, err=%v", got, err)
	}
	if _, err := os.Stat(store.ExecutionHistoryDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created history for unfinished plan: %v", err)
	}
}

func TestExecutionHistorySurvivesClearingAndDeletingSession(t *testing.T) {
	store := Store{Root: t.TempDir(), SessionID: "session-a"}
	if err := store.Save(Session{RunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveExecution(completedExecutionCheckpoint()); err != nil {
		t.Fatal(err)
	}
	path, err := store.ArchiveExecution()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveExecution(testExecutionCheckpoint()); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearExecution(); err != nil {
		t.Fatal(err)
	}
	if err := DeleteSession(store.Root, store.SessionID, "test execution history"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.SessionDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session directory survived deletion: %v", err)
	}
	if body, err := os.ReadFile(path); err != nil || !bytes.Equal(body, before) {
		t.Fatalf("session deletion changed execution history: body=%s, err=%v", body, err)
	}
}

func TestExecutionHistoryDoesNotChangeLoomRetentionPolicy(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if err := store.SaveExecution(completedExecutionCheckpoint()); err != nil {
		t.Fatal(err)
	}
	if refs, err := LoomReferencesAt(context.Background(), store.Root); err != nil || len(refs) != 1 {
		t.Fatalf("active checkpoint refs=%v, err=%v", refs, err)
	}
	if _, err := store.ArchiveExecution(); err != nil {
		t.Fatal(err)
	}
	if refs, err := LoomReferencesAt(context.Background(), store.Root); err != nil || len(refs) != 0 {
		t.Fatalf("completed log unexpectedly pins Loom artifacts: refs=%v, err=%v", refs, err)
	}
}
