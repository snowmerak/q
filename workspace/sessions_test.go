package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/loom"
)

func TestNewSessionIDIsUUID(t *testing.T) {
	value, err := NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !pattern.MatchString(value) {
		t.Fatalf("session ID = %q", value)
	}
}

func TestSessionStoresIsolateConversationAndExecution(t *testing.T) {
	root := t.TempDir()
	first := Store{Root: root, SessionID: "11111111-1111-4111-8111-111111111111"}
	second := Store{Root: root, SessionID: "22222222-2222-4222-8222-222222222222"}
	older := time.Date(2026, time.August, 26, 1, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	if err := first.Save(Session{RunID: "run-first", UpdatedAt: &older, Transcript: []client.Message{{Role: client.RoleUser, Content: "first"}}}); err != nil {
		t.Fatal(err)
	}
	if err := second.Save(Session{RunID: "run-second", UpdatedAt: &newer, Transcript: []client.Message{{Role: client.RoleUser, Content: "second"}}}); err != nil {
		t.Fatal(err)
	}
	firstExecution := testExecutionCheckpoint()
	firstExecution.RunID = "run-first"
	secondExecution := testExecutionCheckpoint()
	secondExecution.RunID = "run-second"
	secondExecution.ExecutionID = "plan-execution-2"
	if err := first.SaveExecution(firstExecution); err != nil {
		t.Fatal(err)
	}
	if err := second.SaveExecution(secondExecution); err != nil {
		t.Fatal(err)
	}

	entries, err := (Store{Root: root}).ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Store.SessionID != second.SessionID || entries[1].Store.SessionID != first.SessionID {
		t.Fatalf("sessions = %#v", entries)
	}
	if filepath.Dir(first.Path()) == filepath.Dir(second.Path()) || filepath.Dir(first.ExecutionPath()) == filepath.Dir(second.ExecutionPath()) {
		t.Fatal("session projections share a directory")
	}
	if err := first.ClearSession(); err != nil {
		t.Fatal(err)
	}
	loaded, err := second.Load()
	if err != nil || len(loaded.Transcript) != 1 || loaded.Transcript[0].Content != "second" {
		t.Fatalf("second session after first clear = %#v, %v", loaded, err)
	}
	if execution, err := second.LoadExecution(); err != nil || execution.ExecutionID != "plan-execution-2" {
		t.Fatalf("second execution after first clear = %#v, %v", execution, err)
	}
}

func TestSessionLocksAllowDifferentSessionsOnly(t *testing.T) {
	root := t.TempDir()
	first, firstLock, err := CreateSession(root, "first")
	if err != nil {
		t.Fatal(err)
	}
	defer firstLock.Close()
	if contender, err := AcquireSessionLock(root, first.SessionID, "contender"); !errors.Is(err, ErrLocked) {
		if contender != nil {
			_ = contender.Close()
		}
		t.Fatalf("same-session lock error = %v", err)
	}
	second, secondLock, err := CreateSession(root, "second")
	if err != nil {
		t.Fatal(err)
	}
	defer secondLock.Close()
	if second.SessionID == first.SessionID {
		t.Fatal("different sessions reused an ID")
	}
}

func TestOpenLatestSessionCreatesWhenNewestIsActive(t *testing.T) {
	root := t.TempDir()
	first, firstLock, err := CreateSession(root, "first")
	if err != nil {
		t.Fatal(err)
	}
	defer firstLock.Close()
	second, secondLock, err := OpenLatestSession(root, "second")
	if err != nil {
		t.Fatal(err)
	}
	if second.SessionID == first.SessionID {
		t.Fatal("active newest session was reused")
	}
	if err := secondLock.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, reopenedLock, err := OpenLatestSession(root, "reopened")
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedLock.Close()
	if reopened.SessionID != second.SessionID {
		t.Fatalf("reopened session = %q, want newest inactive %q", reopened.SessionID, second.SessionID)
	}
}

func TestMigrateLegacySessionSplitsMismatchedPlanRun(t *testing.T) {
	root := t.TempDir()
	legacy := Store{Root: root}
	updatedAt := time.Date(2026, time.August, 26, 2, 0, 0, 0, time.UTC)
	if err := legacy.Save(Session{
		RunID: "run-chat", UpdatedAt: &updatedAt,
		Transcript: []client.Message{{Role: client.RoleUser, Content: "legacy chat"}},
	}); err != nil {
		t.Fatal(err)
	}
	execution := testExecutionCheckpoint()
	execution.RunID = "run-plan"
	if err := legacy.SaveExecution(execution); err != nil {
		t.Fatal(err)
	}
	if err := legacy.MigrateLegacySession(); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy session after migration error = %v", err)
	}
	if _, err := legacy.LoadExecution(); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("legacy execution after migration error = %v", err)
	}
	entries, err := legacy.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("migrated sessions = %#v", entries)
	}
	runs := map[string]Session{}
	for _, entry := range entries {
		loaded, err := entry.Store.Load()
		if err != nil {
			t.Fatal(err)
		}
		runs[loaded.RunID] = loaded
	}
	if len(runs["run-chat"].Transcript) != 1 || runs["run-plan"].Title == "" {
		t.Fatalf("migrated run projections = %#v", runs)
	}
	planStore, err := legacy.ForSession(runs["run-plan"].ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded, err := planStore.LoadExecution(); err != nil || loaded.RunID != "run-plan" {
		t.Fatalf("migrated plan = %#v, %v", loaded, err)
	}
	// Migration is idempotent after the legacy projections have gone.
	if err := legacy.MigrateLegacySession(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateLegacySessionWaitsForLegacyOwnerBeforeCheckingFiles(t *testing.T) {
	root := t.TempDir()
	legacyLock, err := AcquireLock(root, "old q")
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(60 * time.Millisecond)
		_ = legacyLock.Close()
		close(released)
	}()
	started := time.Now()
	if err := (Store{Root: root}).MigrateLegacySession(); err != nil {
		t.Fatal(err)
	}
	<-released
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
		t.Fatalf("migration skipped active legacy owner; elapsed %s", elapsed)
	}
}

func TestDeterministicLegacySessionIDIgnoresVolatileMetadata(t *testing.T) {
	firstTime := time.Date(2026, time.August, 26, 1, 0, 0, 0, time.UTC)
	secondTime := firstTime.Add(time.Hour)
	first, err := deterministicLegacySessionID(Session{
		ID: "old-id", RunID: "run-legacy", UpdatedAt: &firstTime,
		Transcript: []client.Message{{Role: client.RoleUser, Content: "legacy"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := deterministicLegacySessionID(Session{
		ID: "different-id", RunID: "run-legacy", UpdatedAt: &secondTime,
		Transcript: []client.Message{{Role: client.RoleUser, Content: "legacy"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("deterministic IDs differ across volatile metadata: %q != %q", first, second)
	}
}

func TestMigrateLegacySessionWaitsForTargetSessionLock(t *testing.T) {
	root := t.TempDir()
	legacy := Store{Root: root}
	updatedAt := time.Date(2026, time.August, 26, 2, 0, 0, 0, time.UTC)
	if err := legacy.Save(Session{
		RunID: "run-locked", UpdatedAt: &updatedAt,
		Transcript: []client.Message{{Role: client.RoleUser, Content: "legacy"}},
	}); err != nil {
		t.Fatal(err)
	}
	targetLock, err := AcquireSessionLock(root, "locked", "active new q")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	started := time.Now()
	go func() { result <- legacy.MigrateLegacySession() }()
	time.Sleep(60 * time.Millisecond)
	if err := targetLock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
		t.Fatalf("migration did not wait for target session lock; elapsed %s", elapsed)
	}
}

func TestMigrateLegacySessionPreservesDivergentTargetInDeterministicSibling(t *testing.T) {
	root := t.TempDir()
	legacy := Store{Root: root}
	target := Store{Root: root, SessionID: "chat"}
	updatedAt := time.Date(2026, time.August, 26, 2, 0, 0, 0, time.UTC)
	newerAt := updatedAt.Add(time.Hour)
	if err := target.Save(Session{
		RunID: "run-chat", UpdatedAt: &newerAt,
		Transcript: []client.Message{{Role: client.RoleUser, Content: "new target"}},
	}); err != nil {
		t.Fatal(err)
	}
	legacySession := Session{
		RunID: "run-chat", UpdatedAt: &updatedAt,
		Transcript: []client.Message{{Role: client.RoleUser, Content: "legacy conflict"}},
	}
	if err := legacy.Save(legacySession); err != nil {
		t.Fatal(err)
	}
	if err := legacy.MigrateLegacySession(); err != nil {
		t.Fatal(err)
	}
	entries, err := legacy.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("sessions after conflict migration = %#v", entries)
	}
	var siblingID string
	for _, entry := range entries {
		loaded, err := entry.Store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Transcript[0].Content == "legacy conflict" {
			siblingID = entry.Store.SessionID
		}
	}
	if siblingID == "" || siblingID == target.SessionID {
		t.Fatalf("legacy conflict sibling = %q", siblingID)
	}

	// Replaying the same interrupted migration must reuse the same sibling.
	if err := legacy.Save(legacySession); err != nil {
		t.Fatal(err)
	}
	if err := legacy.MigrateLegacySession(); err != nil {
		t.Fatal(err)
	}
	entries, err = legacy.ListSessions()
	if err != nil || len(entries) != 2 {
		t.Fatalf("sessions after migration retry = %#v, %v", entries, err)
	}
	if loaded, err := (Store{Root: root, SessionID: siblingID}).Load(); err != nil || loaded.Transcript[0].Content != "legacy conflict" {
		t.Fatalf("deterministic sibling after retry = %#v, %v", loaded, err)
	}
}

func TestMigrateLegacySessionRetryReusesSiblingAfterLegacyExecutionWasCleared(t *testing.T) {
	root := t.TempDir()
	legacy := Store{Root: root}
	preferred := Store{Root: root, SessionID: "chat"}
	updatedAt := time.Date(2026, time.August, 26, 2, 0, 0, 0, time.UTC)
	if err := preferred.Save(Session{
		RunID: "run-chat", UpdatedAt: &updatedAt,
		Transcript: []client.Message{{Role: client.RoleUser, Content: "divergent target"}},
	}); err != nil {
		t.Fatal(err)
	}
	legacySession := Session{
		RunID: "run-chat", UpdatedAt: &updatedAt,
		Transcript: []client.Message{{Role: client.RoleUser, Content: "legacy"}},
	}
	execution := testExecutionCheckpoint()
	execution.RunID = "run-chat"
	if err := legacy.Save(legacySession); err != nil {
		t.Fatal(err)
	}
	if err := legacy.SaveExecution(execution); err != nil {
		t.Fatal(err)
	}

	// Simulate a crash after publishing the conflict sibling and clearing only
	// the legacy execution projection.
	target, err := prepareLegacyMigrationTarget(root, legacySession, &execution, false, map[string]struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if target.writeSession {
		if err := target.store.Save(target.session); err != nil {
			t.Fatal(err)
		}
	}
	if target.writeExecution {
		if err := target.store.SaveExecution(execution); err != nil {
			t.Fatal(err)
		}
	}
	siblingID := target.store.SessionID
	if err := target.lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := legacy.ClearExecution(); err != nil {
		t.Fatal(err)
	}

	if err := legacy.MigrateLegacySession(); err != nil {
		t.Fatal(err)
	}
	entries, err := legacy.ListSessions()
	if err != nil || len(entries) != 2 {
		t.Fatalf("sessions after crash retry = %#v, %v", entries, err)
	}
	if loaded, err := (Store{Root: root, SessionID: siblingID}).Load(); err != nil || loaded.Transcript[0].Content != "legacy" {
		t.Fatalf("reused sibling = %#v, %v", loaded, err)
	}
}

func TestLoomReferencesAtAggregatesAllSessionsAndLegacy(t *testing.T) {
	root := t.TempDir()
	firstRef := loom.Ref("loom://11111111111111111111111111111111")
	secondRef := loom.Ref("loom://22222222222222222222222222222222")
	legacyRef := loom.Ref("loom://33333333333333333333333333333333")
	first := Store{Root: root, SessionID: "11111111-1111-4111-8111-111111111111"}
	second := Store{Root: root, SessionID: "22222222-2222-4222-8222-222222222222"}
	legacy := Store{Root: root}
	for _, value := range []struct {
		store Store
		ref   loom.Ref
	}{{first, firstRef}, {second, secondRef}, {legacy, legacyRef}} {
		if err := value.store.Save(Session{Transcript: []client.Message{{Role: client.RoleTool, Content: value.ref.String()}}}); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := LoomReferencesAt(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := []loom.Ref{firstRef, secondRef, legacyRef}
	slices.Sort(refs)
	slices.Sort(want)
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("Loom refs = %#v, want %#v", refs, want)
	}
}

func TestDeleteSessionPreservesOtherSessions(t *testing.T) {
	root := t.TempDir()
	first, firstLock, err := CreateSession(root, "first")
	if err != nil {
		t.Fatal(err)
	}
	second, secondLock, err := CreateSession(root, "second")
	if err != nil {
		t.Fatal(err)
	}
	if err := firstLock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := DeleteSession(root, first.SessionID, "delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session load error = %v", err)
	}
	if _, err := second.Load(); err != nil {
		t.Fatal(err)
	}
	if err := secondLock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, DirectoryName, sessionLocksName)); err != nil {
		t.Fatalf("session lock directory missing: %v", err)
	}
}

func TestDeleteSessionRemovesOrphanExecution(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root, SessionID: "11111111-1111-4111-8111-111111111111"}
	execution := testExecutionCheckpoint()
	execution.RunID = "run-orphan"
	if err := store.SaveExecution(execution); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("orphan session load error = %v", err)
	}
	if err := DeleteSession(root, store.SessionID, "delete orphan"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadExecution(); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("orphan execution load error after delete = %v", err)
	}
}
