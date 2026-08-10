package sessionstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/snowmerak/q/worklock"
)

func TestStoreSaveGetUpdateAndReopen(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	saved, err := store.Save(Record{
		Kind: KindMessage, RunID: "run-1", Role: "user", CreatedAt: created,
		UpdatedAt: created, Content: "remember the planner approval", Tags: []string{"decision"},
		Payload: json.RawMessage(`{"choice":"approve"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || saved.Version != RecordVersion {
		t.Fatalf("saved record = %#v", saved)
	}
	if saved.CreatedAt.Location() != time.UTC {
		t.Fatalf("created_at location = %v", saved.CreatedAt.Location())
	}
	if _, err := os.Stat(filepath.Join(root, ".q", "data", "records", recordFileName(saved.ID))); err != nil {
		t.Fatalf("source record was not persisted: %v", err)
	}

	updatedAt := created.Add(time.Hour)
	updated, err := store.Save(Record{
		ID: saved.ID, Kind: KindMessage, RunID: "run-1", Role: "user",
		UpdatedAt: updatedAt, Content: "planner approval was confirmed", Tags: []string{"decision", "approved"},
		Payload: json.RawMessage(`{"choice":"approve"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.CreatedAt.Equal(created) || !updated.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("updated timestamps = %s, %s", updated.CreatedAt, updated.UpdatedAt)
	}
	updated.Tags[0] = "mutated"
	updated.Payload[0] = '['
	loaded, err := store.Get(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]string
	if err := json.Unmarshal(loaded.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if loaded.Tags[0] != "decision" || loaded.Content != "planner approval was confirmed" || payload["choice"] != "approve" {
		t.Fatalf("loaded record = %#v", loaded)
	}
	if _, err := store.Save(Record{
		ID: saved.ID, Kind: KindEvent, CreatedAt: created.Add(time.Minute), UpdatedAt: updatedAt.Add(time.Minute),
	}); err == nil {
		t.Fatal("Save accepted changes to immutable record fields")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	result, err := reopened.Search(context.Background(), SearchOptions{Text: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Hits) != 1 || result.Hits[0].Record.ID != saved.ID {
		t.Fatalf("search result = %#v", result)
	}
}

func TestSearchFiltersDatesAndSorts(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	newer := old.AddDate(1, 0, 0)
	oldRecord, err := store.Save(Record{
		ID: "old", Kind: KindMessage, RunID: "run-a", Role: "planner", Status: "succeeded",
		CreatedAt: old, UpdatedAt: old, Summary: "approval decision", Tags: []string{"decision"},
	})
	if err != nil {
		t.Fatal(err)
	}
	newRecord, err := store.Save(Record{
		ID: "new", Kind: KindMessage, RunID: "run-a", Role: "advisor", Status: "succeeded",
		CreatedAt: newer, UpdatedAt: newer, Content: "approval decision", Tags: []string{"decision"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(Record{
		ID: "unrelated", Kind: KindEvent, RunID: "run-b", Role: "coder", Status: "failed",
		CreatedAt: newer.Add(time.Hour), UpdatedAt: newer.Add(time.Hour), Content: "compiler output",
	}); err != nil {
		t.Fatal(err)
	}

	after := old.Add(time.Hour)
	result, err := store.Search(context.Background(), SearchOptions{
		Text: "approval", Filters: Filters{
			RunIDs: []string{"run-a"}, Kinds: []string{KindMessage},
			Roles: []string{"planner", "advisor"}, Tags: []string{"decision"},
		},
		CreatedAfter: &after,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Hits) != 1 || result.Hits[0].Record.ID != newRecord.ID {
		t.Fatalf("filtered result = %#v", result)
	}

	all, err := store.Search(context.Background(), SearchOptions{
		Filters: Filters{RunIDs: []string{"run-a"}}, Sort: SortNewest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Hits) != 2 || all.Hits[0].Record.ID != newRecord.ID || all.Hits[1].Record.ID != oldRecord.ID {
		t.Fatalf("newest result = %#v", all)
	}

	inclusive, err := store.Search(context.Background(), SearchOptions{
		Filters: Filters{RunIDs: []string{"run-a"}}, CreatedAfter: &old, CreatedBefore: &old,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inclusive.Total != 1 || inclusive.Hits[0].Record.ID != oldRecord.ID {
		t.Fatalf("inclusive date result = %#v", inclusive)
	}
}

func TestRecencyRerankingUsesHalfLifeDecay(t *testing.T) {
	now := time.Date(2026, time.January, 10, 0, 0, 0, 0, time.UTC)
	hits := []Hit{
		{Record: Record{ID: "old", CreatedAt: now.Add(-30 * 24 * time.Hour)}, Score: 2, BaseScore: 2},
		{Record: Record{ID: "new", CreatedAt: now}, Score: 1.5, BaseScore: 1.5},
	}
	rerankByRecency(hits, Recency{Weight: 1, HalfLife: 24 * time.Hour, Now: now})
	if hits[0].Record.ID != "new" || hits[0].Score <= hits[0].BaseScore {
		t.Fatalf("reranked hits = %#v", hits)
	}
}

func TestSaveKeepsSourceWhenIndexingFailsAndOpenCatchesUp(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.index.Close(); err != nil {
		t.Fatal(err)
	}
	record, err := store.Save(Record{ID: "recover-me", Kind: KindResult, Content: "durable result"})
	var indexingError *IndexingError
	if !errors.As(err, &indexingError) {
		t.Fatalf("Save() error = %v, want IndexingError", err)
	}
	if record.ID != "recover-me" {
		t.Fatalf("saved record = %#v", record)
	}
	if _, statErr := os.Stat(store.recordPath(record.ID)); statErr != nil {
		t.Fatalf("source record missing after index failure: %v", statErr)
	}
	store.mu.Lock()
	store.index = nil
	store.mu.Unlock()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	result, err := reopened.Search(context.Background(), SearchOptions{Text: "durable"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Hits[0].Record.ID != record.ID {
		t.Fatalf("recovered result = %#v", result)
	}
}

func TestRebuildRestoresDeletedDerivedIndex(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(Record{ID: "kept", Kind: KindSummary, Content: "persistent summary"}); err != nil {
		t.Fatal(err)
	}
	indexPath := store.IndexPath()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(indexPath); err != nil {
		t.Fatal(err)
	}

	rebuilt, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rebuilt.Close()
	if err := rebuilt.Rebuild(); err != nil {
		t.Fatal(err)
	}
	result, err := rebuilt.Search(context.Background(), SearchOptions{Text: "persistent"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Hits[0].Record.ID != "kept" {
		t.Fatalf("rebuilt result = %#v", result)
	}
}

func TestOpenRejectsASecondWorkspaceWriterWithoutTouchingActiveIndex(t *testing.T) {
	root := t.TempDir()
	first, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Save(Record{ID: "kept", Kind: KindSummary, Content: "active index remains usable"}); err != nil {
		t.Fatal(err)
	}

	second, err := Open(root)
	if second != nil {
		_ = second.Close()
		t.Fatal("second Session Store opened the active workspace")
	}
	if !errors.Is(err, worklock.ErrLocked) {
		t.Fatalf("second Open() error = %v, want worklock.ErrLocked", err)
	}
	result, err := first.Search(context.Background(), SearchOptions{Text: "usable"})
	if err != nil {
		t.Fatalf("active index was disturbed: %v", err)
	}
	if result.Total != 1 || result.Hits[0].Record.ID != "kept" {
		t.Fatalf("active index result = %#v", result)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatalf("Open() after owner close: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenDoesNotRebuildAnIndexLockedByALegacyWriter(t *testing.T) {
	root := t.TempDir()
	legacy, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Save(Record{ID: "kept", Kind: KindSummary, Content: "legacy owner remains usable"}); err != nil {
		t.Fatal(err)
	}
	// Simulate a q version that holds Bleve without participating in the new
	// workspace ownership protocol.
	legacy.mu.Lock()
	if legacy.lock == nil {
		legacy.mu.Unlock()
		t.Fatal("Session Store did not own a workspace lock")
	}
	if err := legacy.lock.Close(); err != nil {
		legacy.mu.Unlock()
		t.Fatal(err)
	}
	legacy.lock = nil
	legacy.mu.Unlock()

	started := time.Now()
	contender, err := Open(root)
	if contender != nil {
		_ = contender.Close()
		_ = legacy.Close()
		t.Fatal("contender opened a legacy-locked Bleve index")
	}
	if !errors.Is(err, ErrIndexLocked) {
		_ = legacy.Close()
		t.Fatalf("contender error = %v, want ErrIndexLocked", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		_ = legacy.Close()
		t.Fatalf("legacy lock detection took %s", elapsed)
	}
	result, err := legacy.Search(context.Background(), SearchOptions{Text: "usable"})
	if err != nil {
		_ = legacy.Close()
		t.Fatalf("legacy index was disturbed: %v", err)
	}
	if result.Total != 1 || result.Hits[0].Record.ID != "kept" {
		_ = legacy.Close()
		t.Fatalf("legacy index result = %#v", result)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseDoesNotReleaseCallerOwnedWorkspaceLock(t *testing.T) {
	root := t.TempDir()
	owner, err := worklock.Acquire(root, "q test")
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenWithOptions(root, OpenOptions{WorkspaceLock: owner})
	if err != nil {
		_ = owner.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		_ = owner.Close()
		t.Fatal(err)
	}
	contender, err := worklock.Acquire(root, "q contender")
	if contender != nil {
		_ = contender.Close()
		_ = owner.Close()
		t.Fatal("Session Store released its caller-owned workspace lock")
	}
	if !errors.Is(err, worklock.ErrLocked) {
		_ = owner.Close()
		t.Fatalf("contender error = %v, want worklock.ErrLocked", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSearchValidation(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.Search(context.Background(), SearchOptions{
		Sort: SortNewest, Recency: &Recency{Weight: 1, HalfLife: time.Hour},
	})
	if err == nil {
		t.Fatal("Search accepted recency weighting with newest sort")
	}
	if _, err := store.Search(nil, SearchOptions{}); err == nil {
		t.Fatal("Search accepted a nil context")
	}
}

func TestLoomReferencesIncludesExplicitAndLegacyRecords(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := "loom://0123456789abcdef0123456789abcdef"
	second := "loom://fedcba9876543210fedcba9876543210"
	if _, err := store.Save(Record{Kind: KindResult, Refs: []string{first}, Content: `{"loom_ref":"` + second + `"}`}); err != nil {
		t.Fatal(err)
	}
	refs, err := store.LoomReferences(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(refs, []string{first, second}) {
		t.Fatalf("Loom refs = %#v", refs)
	}
}
