package blevehnsw_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/contracttest"
	"github.com/snowmerak/q/v2/pkg/storage/search/blevehnsw"
)

func TestBleveHNSWContract(t *testing.T) {
	contracttest.RunSearchContractTests(t, func(t *testing.T) storage.SearchIndex {
		t.Helper()
		idx, err := blevehnsw.Open(filepath.Join(t.TempDir(), "index"))
		if err != nil {
			t.Fatalf("blevehnsw.Open: %v", err)
		}
		return idx
	})
}

// TestReopenAndAppendDoesNotPanic verifies that reopening a persisted HNSW
// graph and appending or updating vectors safely rebuilds an appendable graph
// without triggering goformersearch's nil-RNG bug.
func TestReopenAndAppendDoesNotPanic(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "index")

	idx1, err := blevehnsw.Open(dir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := idx1.ConfigureVector(ctx, "emb", 3); err != nil {
		t.Fatalf("ConfigureVector: %v", err)
	}
	if err := idx1.Upsert(ctx, storage.SearchDocument{
		ID: "doc-1", EntityType: "memory", Scope: "room:1",
		ObservedBy: []string{"aria"}, Text: "first memory",
		OccurredAt: 1000, Revision: 1, Vector: []float32{1, 0, 0},
	}); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if err := idx1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	idx2, err := blevehnsw.Open(dir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = idx2.Close() }()

	if err := idx2.Upsert(ctx, storage.SearchDocument{
		ID: "doc-2", EntityType: "memory", Scope: "room:1",
		ObservedBy: []string{"aria"}, Text: "second memory",
		OccurredAt: 2000, Revision: 1, Vector: []float32{0, 1, 0},
	}); err != nil {
		t.Fatalf("append after reopen: %v", err)
	}

	hits, err := idx2.Search(ctx, storage.SearchQuery{
		Vector:     []float32{0, 1, 0},
		Mode:       "semantic",
		ObserverID: "aria",
		Limit:      5,
	})
	if err != nil {
		t.Fatalf("semantic search after reopen: %v", err)
	}
	if len(hits) != 2 || hits[0].ID != "doc-2" {
		t.Fatalf("unexpected hits after reopen: %+v", hits)
	}
}
