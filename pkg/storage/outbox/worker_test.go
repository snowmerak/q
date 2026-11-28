package outbox_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/outbox"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/sqlite"
	"github.com/snowmerak/q/v2/pkg/storage/search/blevehnsw"
)

type stubEmbedder struct {
	fail bool
}

func (e *stubEmbedder) EmbedDocument(_ context.Context, _ string) (string, int, []float32, error) {
	if e.fail {
		return "", 0, nil, errors.New("embedding server offline")
	}
	return "stub-emb", 3, []float32{0.1, 0.9, 0}, nil
}

func TestOutboxWorkerDegradesToLexicalOnEmbeddingFailureAndRecovers(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	rdb, err := sqlite.Open(ctx, filepath.Join(tmp, "q.sqlite"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer func() { _ = rdb.Close() }()

	idx, err := blevehnsw.Open(filepath.Join(tmp, "index"))
	if err != nil {
		t.Fatalf("blevehnsw.Open: %v", err)
	}
	defer func() { _ = idx.Close() }()

	_, err = rdb.Outbox().Enqueue(ctx, domain.OutboxEntry{
		EntityType:  "memory",
		EntityID:    "mem-1",
		Op:          domain.OutboxOpUpsert,
		Revision:    1,
		PayloadJSON: `{"scope":"room:1","owner_id":"aria","observed_by":["aria"],"text":"starlight lantern","occurred_at":1000}`,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	emb := &stubEmbedder{fail: true}
	worker := outbox.NewWorker(rdb, idx, emb)
	n, err := worker.ProcessOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("ProcessOnce with failing embedder: n=%d err=%v", n, err)
	}

	// Lexical search must already find the document even though embedding failed.
	hits, err := idx.Search(ctx, storage.SearchQuery{
		Text:       "lantern",
		Mode:       "hybrid",
		ObserverID: "aria",
		Limit:      5,
	})
	if err != nil || len(hits) != 1 || hits[0].ID != "mem-1" {
		t.Fatalf("expected lexical fallback hit mem-1, got %+v err=%v", hits, err)
	}

	// Outbox entry should be scheduled as embedding_retry.
	// Advance next_attempt_at via MarkRetry or test recovery once embedder recovers.
	pending, err := rdb.Outbox().ListPending(ctx, domain.NowMS()+10_000, 10)
	if err != nil || len(pending) != 1 || pending[0].Status != domain.OutboxStatusEmbeddingRetry {
		t.Fatalf("expected embedding_retry status, got %+v err=%v", pending, err)
	}

	// Recover embedder and run again with future timestamp window; wait—ListPending checks next_attempt_at <= nowMS.
	// Mark retry due immediately by re-testing via MarkRetry with nowMS-1, then ProcessOnce.
	if err := rdb.Outbox().MarkRetry(ctx, pending[0].Seq, domain.OutboxStatusEmbeddingRetry, domain.NowMS()-1, ""); err != nil {
		t.Fatalf("MarkRetry: %v", err)
	}
	emb.fail = false
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("ProcessOnce recovery: %v", err)
	}

	docs, vecs, err := idx.Counts(ctx)
	if err != nil || docs != 1 || vecs != 1 {
		t.Fatalf("expected (1 doc, 1 vector) after recovery, got (%d, %d, %v)", docs, vecs, err)
	}
}
