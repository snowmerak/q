// Package outbox drains index_outbox entries from RDBStore into SearchIndex,
// degrading gracefully to lexical-only indexing when embeddings fail transiently.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/storage"
)

// Embedder generates embedding vectors for document text.
type Embedder interface {
	EmbedDocument(ctx context.Context, text string) (model string, dim int, vector []float32, err error)
}

type Payload struct {
	Scope      string    `json:"scope"`
	OwnerID    string    `json:"owner_id"`
	ObservedBy []string  `json:"observed_by,omitempty"`
	Text       string    `json:"text"`
	OccurredAt int64     `json:"occurred_at"`
	Vector     []float32 `json:"vector,omitempty"`
}

type Worker struct {
	rdb          storage.RDBStore
	search       storage.SearchIndex
	embedder     Embedder
	pollInterval time.Duration
	batchSize    int
	maxAttempts  int

	mu      sync.Mutex
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	onError func(error)
}

type Option func(*Worker)

func WithPollInterval(d time.Duration) Option {
	return func(w *Worker) {
		if d > 0 {
			w.pollInterval = d
		}
	}
}

func WithErrorHandler(fn func(error)) Option {
	return func(w *Worker) {
		w.onError = fn
	}
}

func NewWorker(rdb storage.RDBStore, search storage.SearchIndex, embedder Embedder, opts ...Option) *Worker {
	w := &Worker{
		rdb:          rdb,
		search:       search,
		embedder:     embedder,
		pollInterval: 500 * time.Millisecond,
		batchSize:    32,
		maxAttempts:  8,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(w)
		}
	}
	return w
}

// Start runs the background polling loop until Stop is called or ctx is cancelled.
func (w *Worker) Start(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		ticker := time.NewTicker(w.pollInterval)
		defer ticker.Stop()
		for {
			if _, err := w.ProcessOnce(runCtx); err != nil && runCtx.Err() == nil && w.onError != nil {
				w.onError(err)
			}
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// Stop cancels the background worker and waits for active processing to exit.
func (w *Worker) Stop() {
	w.mu.Lock()
	cancel := w.cancel
	w.cancel = nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	w.wg.Wait()
}

// ProcessOnce drains up to one batch of due outbox entries and returns how many
// entries were handled.
func (w *Worker) ProcessOnce(ctx context.Context) (int, error) {
	if w == nil || w.rdb == nil || w.search == nil {
		return 0, nil
	}
	entries, err := w.rdb.Outbox().ListPending(ctx, domain.NowMS(), w.batchSize)
	if err != nil {
		return 0, fmt.Errorf("list pending outbox: %w", err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := w.handleEntry(ctx, entry); err != nil {
			status := domain.OutboxStatusPending
			if entry.Attempts+1 >= w.maxAttempts {
				status = domain.OutboxStatusDead
			}
			delay := retryBackoff(entry.Attempts + 1)
			_ = w.rdb.Outbox().MarkRetry(ctx, entry.Seq, status, domain.NowMS()+delay.Milliseconds(), err.Error())
			continue
		}
	}
	return len(entries), nil
}

func (w *Worker) handleEntry(ctx context.Context, entry domain.OutboxEntry) error {
	if entry.Op == domain.OutboxOpDelete {
		if err := w.search.Delete(ctx, entry.EntityType, entry.EntityID); err != nil {
			return err
		}
		return w.rdb.Outbox().MarkDone(ctx, entry.Seq)
	}

	var payload Payload
	if entry.PayloadJSON != "" {
		if err := json.Unmarshal([]byte(entry.PayloadJSON), &payload); err != nil {
			// Malformed payload is non-retryable.
			return w.rdb.Outbox().MarkRetry(ctx, entry.Seq, domain.OutboxStatusDead, domain.NowMS(), err.Error())
		}
	}

	doc := storage.SearchDocument{
		ID:         entry.EntityID,
		EntityType: entry.EntityType,
		Scope:      payload.Scope,
		OwnerID:    payload.OwnerID,
		ObservedBy: payload.ObservedBy,
		Text:       payload.Text,
		OccurredAt: payload.OccurredAt,
		Revision:   entry.Revision,
		Vector:     payload.Vector,
	}

	var embErr error
	if doc.Vector == nil && w.embedder != nil && payload.Text != "" {
		model, dim, vec, err := w.embedder.EmbedDocument(ctx, payload.Text)
		if err != nil {
			embErr = err
		} else if len(vec) > 0 && dim > 0 {
			_ = w.search.ConfigureVector(ctx, model, dim)
			doc.Vector = vec
		}
	}

	if err := w.search.Upsert(ctx, doc); err != nil {
		return err
	}
	if embErr != nil {
		// Lexical projection succeeded; schedule vector retry without blocking.
		status := domain.OutboxStatusEmbeddingRetry
		if entry.Attempts+1 >= w.maxAttempts {
			status = domain.OutboxStatusDead
		}
		delay := retryBackoff(entry.Attempts + 1)
		return w.rdb.Outbox().MarkRetry(ctx, entry.Seq, status, domain.NowMS()+delay.Milliseconds(), embErr.Error())
	}
	return w.rdb.Outbox().MarkDone(ctx, entry.Seq)
}

func retryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<uint(attempt-1)) * time.Second
}
