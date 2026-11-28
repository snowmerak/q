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

func TestResetAndRebuildCancelsAndPreservesGeneration(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "index")
	idx, err := blevehnsw.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idx.Close() }()

	if err := idx.Upsert(ctx, storage.SearchDocument{
		ID: "doc-keep", EntityType: "memory", Scope: "room:r1",
		ObservedBy: []string{"aria"}, Text: "existing coastal lighthouse", OccurredAt: 1000, Revision: 1,
	}); err != nil {
		t.Fatal(err)
	}

	jobCtx, cancel := context.WithCancel(ctx)
	docsCh := make(chan storage.SearchDocument, 4)
	docsCh <- storage.SearchDocument{
		ID: "doc-partial", EntityType: "memory", Scope: "room:r1",
		ObservedBy: []string{"aria"}, Text: "partial incomplete document", OccurredAt: 2000, Revision: 1,
	}
	cancel()
	close(docsCh)

	rebuildErr := idx.ResetAndRebuild(jobCtx, docsCh)
	if rebuildErr == nil {
		t.Fatal("expected cancelled ResetAndRebuild to fail")
	}

	keepHits, err := idx.Search(ctx, storage.SearchQuery{
		Text: "coastal lighthouse", Mode: "lexical", ObserverID: "aria", Limit: 5,
	})
	if err != nil || len(keepHits) != 1 || keepHits[0].ID != "doc-keep" {
		t.Fatalf("expected existing doc-keep preserved, got %+v err=%v", keepHits, err)
	}
	partialHits, err := idx.Search(ctx, storage.SearchQuery{
		Text: "incomplete document", Mode: "lexical", ObserverID: "aria", Limit: 5,
	})
	if err != nil || len(partialHits) != 0 {
		t.Fatalf("expected partial document discarded on cancelled rebuild, got %+v err=%v", partialHits, err)
	}
}
