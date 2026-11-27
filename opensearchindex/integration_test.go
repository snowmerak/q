package opensearchindex

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/snowmerak/q/sessionstore"
)

func requireLiveOpenSearch(t *testing.T) (*Client, string) {
	t.Helper()
	endpoint := os.Getenv("Q_TEST_OPENSEARCH_URL")
	if endpoint == "" {
		t.Skip("set Q_TEST_OPENSEARCH_URL to run live OpenSearch integration tests")
	}
	prefix := fmt.Sprintf("qtest-%d", time.Now().UTC().UnixNano())
	client, err := NewClient(Config{
		Endpoint:    endpoint,
		IndexPrefix: prefix,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		_ = client.Close()
		t.Fatalf("OpenSearch ping failed: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = client.doJSON(cleanupCtx, http.MethodDelete, "/"+prefix+"-*", nil, nil)
		_ = client.Close()
	})
	return client, prefix
}

func TestOpenSearchIndexContract(t *testing.T) {
	client, _ := requireLiveOpenSearch(t)
	ctx := t.Context()

	vecCfg := sessionstore.VectorConfig{Model: "test-embed-v1", Dimensions: 3}
	idx, created, err := OpenIndex(ctx, client, "contract-test", vecCfg)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected initial OpenIndex to report created=true")
	}
	defer func() { _ = idx.Close() }()

	t0 := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Millisecond)
	t1 := t0.Add(time.Hour)

	saved, err := idx.SaveBatch(ctx, []sessionstore.Record{
		{
			ID:          "doc-1",
			Kind:        sessionstore.KindEvent,
			WorkspaceID: "ws-a",
			Summary:     "OpenSearch hybrid engine",
			Content:     "Supports BM25 lexical matching and kNN vector retrieval.",
			SearchText:  "opensearch knn bm25",
			Tags:        []string{"search", "engine"},
			CreatedAt:   t0,
			UpdatedAt:   t0,
			Payload:     []byte(`{"source":"unit"}`),
		},
		{
			ID:          "skill-1",
			Kind:        sessionstore.KindSkill,
			Scope:       "global",
			Status:      sessionstore.StatusSucceeded,
			Summary:     "animal-care",
			Content:     "Guide for domestic feline health and feeding.",
			Tags:        []string{"pets", "care"},
			CreatedAt:   t1,
			UpdatedAt:   t1,
		},
	})
	if err != nil || len(saved) != 2 {
		t.Fatalf("SaveBatch = (%d, %v)", len(saved), err)
	}

	got, err := idx.Get(ctx, "doc-1")
	if err != nil || got.Summary != "OpenSearch hybrid engine" || string(got.Payload) != `{"source":"unit"}` {
		t.Fatalf("Get(doc-1) = %#v, err = %v", got, err)
	}

	// Immutable Kind / CreatedAt invariant check.
	if _, err := idx.Save(ctx, sessionstore.Record{ID: "doc-1", Kind: sessionstore.KindResult}); err == nil {
		t.Fatal("expected error when changing record Kind")
	}

	d1, err := sessionstore.RecordSourceDigest(saved[0])
	if err != nil {
		t.Fatal(err)
	}
	d2, err := sessionstore.RecordSourceDigest(saved[1])
	if err != nil {
		t.Fatal(err)
	}
	applyRes, err := idx.ApplyEmbeddings(ctx, []sessionstore.EmbeddingUpdate{
		{
			ID:           "doc-1",
			SourceDigest: d1,
			Embedding:    &sessionstore.Embedding{Model: vecCfg.Model, Dimensions: 3, CreatedAt: t0, Vector: []float32{1, 0, 0}},
		},
		{
			ID:           "skill-1",
			SourceDigest: d2,
			VectorProjections: []sessionstore.VectorProjection{
				{ID: "name", Embedding: sessionstore.Embedding{Model: vecCfg.Model, Dimensions: 3, CreatedAt: t1, Vector: []float32{0, 1, 0}}},
				{ID: "description", Embedding: sessionstore.Embedding{Model: vecCfg.Model, Dimensions: 3, CreatedAt: t1, Vector: []float32{0.1, 0.9, 0.1}}},
			},
		},
		{
			ID:           "missing-doc",
			SourceDigest: "deadbeef",
		},
	})
	if err != nil || applyRes.Updated != 2 || applyRes.Skipped != 1 {
		t.Fatalf("ApplyEmbeddings = %#v, err = %v", applyRes, err)
	}

	docsCount, vecCount, err := idx.IndexCounts(ctx)
	if err != nil || docsCount != 2 || vecCount != 2 {
		t.Fatalf("IndexCounts = (%d, %d, %v), want (2, 2)", docsCount, vecCount, err)
	}

	// Lexical search with filter & boosts.
	lexRes, err := idx.Search(ctx, sessionstore.SearchOptions{
		Text:       "hybrid",
		TextBoosts: &sessionstore.TextFieldBoosts{Summary: 2, Content: 1},
		Filters:    sessionstore.Filters{WorkspaceIDs: []string{"ws-a"}},
	})
	if err != nil || lexRes.Total != 1 || len(lexRes.Hits) != 1 || lexRes.Hits[0].Record.ID != "doc-1" {
		t.Fatalf("lexical Search = %#v, err = %v", lexRes, err)
	}

	// Top-level kNN + HybridTextGate.
	topHybrid, err := idx.Search(ctx, sessionstore.SearchOptions{
		Text: "retrieval",
		Vector: &sessionstore.VectorQuery{
			Embedding: []float32{0.98, 0.02, 0},
		},
		HybridTextGate: &sessionstore.HybridTextGate{
			MinimumVectorScore: 0.8,
			MinimumTextScore:   0.1,
		},
	})
	if err != nil || len(topHybrid.Hits) == 0 || topHybrid.Hits[0].Record.ID != "doc-1" {
		t.Fatalf("top-level hybrid Search = %#v, err = %v", topHybrid, err)
	}

	// Nested VectorProjections kNN with ProjectionWeights.
	nestedRes, err := idx.Search(ctx, sessionstore.SearchOptions{
		Vector: &sessionstore.VectorQuery{
			Embedding: []float32{0.05, 0.98, 0.05},
			ProjectionWeights: map[string]float64{
				"description": 2.0,
				"name":        1.0,
			},
		},
	})
	if err != nil || len(nestedRes.Hits) == 0 || nestedRes.Hits[0].Record.ID != "skill-1" || math.Abs(nestedRes.Hits[0].Score) == 0 {
		t.Fatalf("nested vector projection Search = %#v, err = %v", nestedRes, err)
	}

	// Alias rollover via ConfigureVector to a different dimension.
	if err := idx.ConfigureVector(ctx, sessionstore.VectorConfig{Model: "embed-v2", Dimensions: 4}); err != nil {
		t.Fatalf("ConfigureVector rollover failed: %v", err)
	}
	docsAfter, vecAfter, err := idx.IndexCounts(ctx)
	if err != nil || docsAfter != 2 || vecAfter != 0 {
		t.Fatalf("IndexCounts after rollover = (%d, %d, %v), want (2, 0)", docsAfter, vecAfter, err)
	}

	if err := idx.Delete(ctx, "doc-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Get(ctx, "doc-1"); err != sessionstore.ErrNotFound {
		t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
	}
}
