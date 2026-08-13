package sessionstore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testVectorConfig() VectorConfig {
	return VectorConfig{Model: "embed-test", Dimensions: 3, M: 8, EfConstruction: 64, EfSearch: 32}
}

func TestHNSWVectorSearchPersistsUpdatesAndFilters(t *testing.T) {
	root := t.TempDir()
	store, err := OpenWithOptions(root, OpenOptions{Vector: testVectorConfig()})
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	records := []Record{
		{ID: "planner", Kind: KindMessage, Role: "planner", CreatedAt: created, UpdatedAt: created,
			Content: "planner approval", Embedding: &Embedding{Model: "embed-test", Vector: []float32{1, 0, 0}}},
		{ID: "advisor", Kind: KindMessage, Role: "advisor", CreatedAt: created.Add(time.Minute), UpdatedAt: created.Add(time.Minute),
			Content: "advisor guidance", Embedding: &Embedding{Model: "embed-test", Vector: []float32{0.8, 0.2, 0}}},
		{ID: "coder", Kind: KindResult, Role: "coder", CreatedAt: created.Add(2 * time.Minute), UpdatedAt: created.Add(2 * time.Minute),
			Content: "compiler output", Embedding: &Embedding{Model: "embed-test", Vector: []float32{0, 0, 1}}},
	}
	for _, record := range records {
		if _, err := store.Save(record); err != nil {
			t.Fatal(err)
		}
	}

	result, err := store.Search(context.Background(), SearchOptions{
		Vector: &VectorQuery{Embedding: []float32{1, 0, 0}}, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 3 || len(result.Hits) != 1 || result.Hits[0].Record.ID != "planner" {
		t.Fatalf("vector result = %#v", result)
	}
	filtered, err := store.Search(context.Background(), SearchOptions{
		Vector: &VectorQuery{Embedding: []float32{1, 0, 0}}, Filters: Filters{Roles: []string{"advisor"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 1 || filtered.Hits[0].Record.ID != "advisor" {
		t.Fatalf("filtered vector result = %#v", filtered)
	}

	planner, err := store.Get("planner")
	if err != nil {
		t.Fatal(err)
	}
	planner.UpdatedAt = created.Add(3 * time.Minute)
	planner.Embedding = &Embedding{Model: "embed-test", Vector: []float32{0, 0, 1}}
	if _, err := store.Save(planner); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Search(context.Background(), SearchOptions{
		Vector: &VectorQuery{Embedding: []float32{1, 0, 0}}, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Hits) != 1 || updated.Hits[0].Record.ID != "advisor" {
		t.Fatalf("updated vector result = %#v", updated)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenWithOptions(root, OpenOptions{Vector: testVectorConfig()})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.Search(context.Background(), SearchOptions{
		Vector: &VectorQuery{Embedding: []float32{1, 0, 0}}, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Hits) != 1 || persisted.Hits[0].Record.ID != "advisor" {
		t.Fatalf("persisted vector result = %#v", persisted)
	}
	for _, path := range []string{
		filepath.Join(root, ".q", "index", "vectors.hnsw"),
		filepath.Join(root, ".q", "index", "vectors.ids.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("derived vector file %s: %v", path, err)
		}
	}
}

func TestHNSWHybridSearchFusesTextAndVectorCandidates(t *testing.T) {
	store, err := OpenWithOptions(t.TempDir(), OpenOptions{Vector: testVectorConfig()})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, record := range []Record{
		{ID: "text", Kind: KindResult, Content: "exact compiler diagnostic", Embedding: &Embedding{Model: "embed-test", Vector: []float32{0, 1, 0}}},
		{ID: "semantic", Kind: KindSummary, Content: "build investigation", Embedding: &Embedding{Model: "embed-test", Vector: []float32{1, 0, 0}}},
	} {
		if _, err := store.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.Search(context.Background(), SearchOptions{
		Text: "compiler", Vector: &VectorQuery{Embedding: []float32{1, 0, 0}}, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 2 {
		t.Fatalf("hybrid hits = %#v", result.Hits)
	}
	seen := map[string]Hit{}
	for _, hit := range result.Hits {
		seen[hit.Record.ID] = hit
	}
	if seen["text"].TextScore == 0 || seen["semantic"].VectorScore == 0 {
		t.Fatalf("hybrid branch scores = %#v", result.Hits)
	}
}

func TestHNSWMultipleProjectionsCollapseUpdateAndDelete(t *testing.T) {
	store, err := OpenWithOptions(t.TempDir(), OpenOptions{Vector: testVectorConfig()})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	proposition := Record{
		ID: "prop", Kind: KindProposition, Scope: "global", Content: "canonical", CreatedAt: created, UpdatedAt: created,
		VectorProjections: []VectorProjection{
			{ID: "content", Embedding: Embedding{Model: "embed-test", Vector: []float32{1, 0, 0}}},
			{ID: "query-0", Embedding: Embedding{Model: "embed-test", Vector: []float32{0.99, 0.01, 0}}},
		},
	}
	other := Record{
		ID: "other", Kind: KindProposition, Scope: "global", Content: "other",
		VectorProjections: []VectorProjection{{ID: "content", Embedding: Embedding{Model: "embed-test", Vector: []float32{0.8, 0.2, 0}}}},
	}
	for _, record := range []Record{proposition, other} {
		if _, err := store.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.Search(context.Background(), SearchOptions{
		Vector: &VectorQuery{Embedding: []float32{1, 0, 0}}, Limit: 10,
		Filters: Filters{Kinds: []string{KindProposition}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || len(result.Hits) != 2 || result.Hits[0].Record.ID != "prop" {
		t.Fatalf("collapsed projection result = %#v", result)
	}

	loaded, err := store.Get("prop")
	if err != nil {
		t.Fatal(err)
	}
	loaded.UpdatedAt = created.Add(time.Minute)
	loaded.VectorProjections = []VectorProjection{
		{ID: "content", Embedding: Embedding{Model: "embed-test", Vector: []float32{0, 0, 1}}},
	}
	if _, err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Search(context.Background(), SearchOptions{
		Vector: &VectorQuery{Embedding: []float32{1, 0, 0}}, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Hits) != 1 || updated.Hits[0].Record.ID != "other" {
		t.Fatalf("updated projections = %#v", updated)
	}
	if err := store.Delete("other"); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.Search(context.Background(), SearchOptions{
		Vector: &VectorQuery{Embedding: []float32{1, 0, 0}}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Total != 1 || deleted.Hits[0].Record.ID != "prop" {
		t.Fatalf("deleted projections = %#v", deleted)
	}
}

func TestHNSWCorruptionAndModelChangesRebuildFromRecords(t *testing.T) {
	root := t.TempDir()
	store, err := OpenWithOptions(root, OpenOptions{Vector: testVectorConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(Record{
		ID: "kept", Kind: KindSummary, Embedding: &Embedding{Model: "embed-test", Vector: []float32{1, 0, 0}},
	}); err != nil {
		t.Fatal(err)
	}
	vectorPath := store.VectorIndexPath()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vectorPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}

	rebuilt, err := OpenWithOptions(root, OpenOptions{Vector: testVectorConfig()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := rebuilt.Search(context.Background(), SearchOptions{
		Vector: &VectorQuery{Embedding: []float32{1, 0, 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || result.Hits[0].Record.ID != "kept" {
		t.Fatalf("rebuilt vector result = %#v", result)
	}
	if err := rebuilt.Close(); err != nil {
		t.Fatal(err)
	}

	other := testVectorConfig()
	other.Model = "embed-other"
	changed, err := OpenWithOptions(root, OpenOptions{Vector: other})
	if err != nil {
		t.Fatal(err)
	}
	defer changed.Close()
	empty, err := changed.Search(context.Background(), SearchOptions{
		Vector: &VectorQuery{Embedding: []float32{1, 0, 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Total != 0 || len(empty.Hits) != 0 {
		t.Fatalf("changed-model vector result = %#v", empty)
	}
	stateBody, err := os.ReadFile(filepath.Join(root, ".q", "index", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state indexState
	if err := json.Unmarshal(stateBody, &state); err != nil {
		t.Fatal(err)
	}
	if state.Vector == nil || state.Vector.Model != other.Model || state.Vector.RecordCount != 0 {
		t.Fatalf("vector state = %#v", state.Vector)
	}
}

func TestEmbeddingAndVectorQueryValidation(t *testing.T) {
	store, err := OpenWithOptions(t.TempDir(), OpenOptions{Vector: testVectorConfig()})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Save(Record{
		ID: "zero", Kind: KindMessage, Embedding: &Embedding{Model: "embed-test", Vector: []float32{0, 0, 0}},
	}); err == nil {
		t.Fatal("Save accepted a zero embedding")
	}
	if _, err := store.Search(context.Background(), SearchOptions{
		Vector: &VectorQuery{Embedding: []float32{1, 0}},
	}); err == nil {
		t.Fatal("Search accepted a dimension mismatch")
	}
}
