package archiveembed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/sessionstore"
)

type repairEmbedder struct {
	testEmbedder
	fail bool
}

type changingSourceEmbedder struct {
	testEmbedder
	once   sync.Once
	change func()
}

func TestBackfillVerifiesCoverageAfterDeletingCurrentRecord(t *testing.T) {
	store, err := sessionstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	created := time.Now().UTC().Add(-time.Hour)
	records := make([]sessionstore.Record, backfillPageSize+1)
	for index := range records {
		records[index] = sessionstore.Record{ID: fmt.Sprintf("record-%03d", index), Kind: sessionstore.KindMessage, Content: "cat", CreatedAt: created.Add(time.Duration(index) * time.Second)}
	}
	records[0].Embedding = &sessionstore.Embedding{Model: "current", Dimensions: 3, Vector: []float32{1, 0, 0}}
	if _, err := store.SaveBatch(records); err != nil {
		t.Fatal(err)
	}
	embedder := &changingSourceEmbedder{change: func() {
		// This record is not in the embedding updates, so CAS cannot detect
		// its deletion moving the last record into the preceding offset page.
		if err := store.Delete(records[0].ID); err != nil {
			t.Error(err)
		}
	}}
	archive := New(store)
	if err := archive.Configure(embedder, "current", 3); err != nil {
		t.Fatal(err)
	}
	stats, err := archive.Backfill(t.Context())
	if err != nil || stats.Embedded != backfillPageSize {
		t.Fatalf("repair = %+v, %v", stats, err)
	}
	for _, source := range records[1:] {
		record, err := store.Get(source.ID)
		if err != nil || record.Embedding == nil || record.Embedding.Model != "current" {
			t.Fatalf("unrepaired record = %+v, %v", record, err)
		}
	}
	if _, err := store.Get(records[0].ID); !errors.Is(err, sessionstore.ErrNotFound) {
		t.Fatalf("deleted record resurrected: %v", err)
	}
}

type changingScanStore struct {
	*sessionstore.Store
	scans int
}

func (s *changingScanStore) Search(ctx context.Context, options sessionstore.SearchOptions) (sessionstore.SearchResult, error) {
	result, err := s.Store.Search(ctx, options)
	if err == nil && options.Offset == 0 && len(result.Hits) > 0 {
		s.scans++
		record := result.Hits[0].Record
		record.Refs = []string{fmt.Sprintf("revision-%d", s.scans)}
		_, err = s.Save(record)
	}
	return result, err
}

func TestBackfillRejectsContinuouslyChangingScan(t *testing.T) {
	store, err := sessionstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Save(sessionstore.Record{ID: "changing", Kind: sessionstore.KindMessage, Content: "cat", Embedding: &sessionstore.Embedding{Model: "current", Dimensions: 3, Vector: []float32{1, 0, 0}}}); err != nil {
		t.Fatal(err)
	}
	source := &changingScanStore{Store: store}
	archive := New(source)
	if err := archive.Configure(&testEmbedder{}, "current", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Backfill(t.Context()); err == nil || !strings.Contains(err.Error(), "source kept changing") {
		t.Fatalf("unstable scan reported success: %v", err)
	}
	if source.scans > 5 {
		t.Fatalf("repair did not bound retries: %d scans", source.scans)
	}
}

func TestResolverOperationsCanCancelWhileRepairing(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("model_changed=%t", changed), func(t *testing.T) {
			store, err := sessionstore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if _, err := store.Save(sessionstore.Record{Kind: sessionstore.KindMessage, Content: "cat"}); err != nil {
				t.Fatal(err)
			}
			embedder := &blockingEmbedder{started: make(chan struct{}), release: make(chan struct{})}
			archive := New(store)
			if err := archive.Configure(embedder, "current", 3); err != nil {
				t.Fatal(err)
			}
			wanted := "current"
			archive.SetVectorConfigResolver(func(context.Context) (sessionstore.VectorConfig, error) {
				return sessionstore.VectorConfig{Model: wanted, Dimensions: 3}, nil
			})
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { _, err := archive.Backfill(ctx); done <- err }()
			t.Cleanup(func() { cancel(); <-done })
			select {
			case <-embedder.started:
			case <-time.After(5 * time.Second):
				t.Fatal("repair never started")
			}
			if changed {
				wanted = "next"
			}
			operations := map[string]func(context.Context) error{
				"search": func(ctx context.Context) error {
					_, err := archive.Search(ctx, sessionstore.SearchOptions{Text: "cat"})
					return err
				},
				"backfill": func(ctx context.Context) error { _, err := archive.Backfill(ctx); return err },
				"prepare": func(ctx context.Context) error {
					_, err := archive.Prepare(ctx, []sessionstore.Record{{Kind: sessionstore.KindMessage, Content: "dog"}})
					return err
				},
				"configure": func(ctx context.Context) error { return archive.ConfigureContext(ctx, embedder, "next", 3) },
			}
			for name, operation := range operations {
				t.Run(name, func(t *testing.T) {
					waitCtx, stop := context.WithTimeout(t.Context(), 20*time.Millisecond)
					defer stop()
					returned := make(chan error, 1)
					go func() { returned <- operation(waitCtx) }()
					select {
					case err := <-returned:
						if !errors.Is(err, context.DeadlineExceeded) {
							t.Errorf("waiting operation = %v", err)
						}
					case <-time.After(time.Second):
						t.Error("waiting operation ignored its deadline")
						cancel()
						<-returned
					}
				})
				if ctx.Err() != nil {
					return
				}
			}
			readCtx, stop := context.WithTimeout(t.Context(), time.Second)
			defer stop()
			if current := store.VectorConfig(); current.Model != "current" {
				t.Errorf("cancelled operation changed the model: %+v", current)
			}
			if _, err := archive.Search(readCtx, sessionstore.SearchOptions{Sort: sessionstore.SortOldest}); err != nil {
				t.Errorf("chronological read blocked by provider: %v", err)
			}
		})
	}
}

func TestRunningArchiveFollowsLatestModelAndRejectsOldWorker(t *testing.T) {
	store, err := sessionstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	record, err := store.Save(sessionstore.Record{Kind: sessionstore.KindMessage, Content: "cat"})
	if err != nil {
		t.Fatal(err)
	}
	wanted := sessionstore.VectorConfig{Model: "old", Dimensions: 3}
	provider := &changingSourceEmbedder{change: func() {
		wanted.Model = "new"
		if err := store.ConfigureVector(wanted); err != nil {
			t.Error(err)
		}
	}}
	archive := New(store)
	archive.SetVectorConfigResolver(func(context.Context) (sessionstore.VectorConfig, error) { return wanted, nil })
	if err := archive.Configure(provider, "old", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Search(t.Context(), sessionstore.SearchOptions{Text: "feline"}); err == nil {
		t.Fatal("old worker attached vectors after model transition")
	}
	latest, err := store.Get(record.ID)
	if err != nil || latest.Embedding != nil {
		t.Fatalf("old worker changed vectors: %#v, %v", latest, err)
	}
	if _, err := archive.Search(t.Context(), sessionstore.SearchOptions{Text: "feline"}); err != nil {
		t.Fatal(err)
	}
	latest, err = store.Get(record.ID)
	if err != nil || latest.Embedding.Model != "new" {
		t.Fatalf("next use did not repair latest model: %#v, %v", latest, err)
	}
	wanted = sessionstore.VectorConfig{}
	if _, err := archive.Search(t.Context(), sessionstore.SearchOptions{Text: "feline"}); err != nil {
		t.Fatal(err)
	}
	if store.VectorConfig().Enabled() {
		t.Fatal("archive ignored disabled settings")
	}
	wanted = sessionstore.VectorConfig{Model: "reenabled", Dimensions: 3}
	if _, err := archive.Search(t.Context(), sessionstore.SearchOptions{Text: "feline"}); err != nil {
		t.Fatal(err)
	}
	latest, err = store.Get(record.ID)
	if err != nil || latest.Embedding.Model != "reenabled" {
		t.Fatalf("archive did not re-enable on use: %#v, %v", latest, err)
	}
}

func (e *changingSourceEmbedder) Embed(ctx context.Context, input client.EmbeddingRequest) (*client.EmbeddingResponse, error) {
	e.once.Do(e.change)
	return e.testEmbedder.Embed(ctx, input)
}

func TestRepairCannotResurrectDeletedOrOverwriteChangedSource(t *testing.T) {
	for _, deletion := range []bool{true, false} {
		name := "edit"
		if deletion {
			name = "delete"
		}
		t.Run(name, func(t *testing.T) {
			store, err := sessionstore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			record, err := store.Save(sessionstore.Record{Kind: sessionstore.KindMessage, Content: "old cat"})
			if err != nil {
				t.Fatal(err)
			}
			provider := &changingSourceEmbedder{change: func() {
				if deletion {
					if err := store.Delete(record.ID); err != nil {
						t.Error(err)
					}
					return
				}
				latest, err := store.Get(record.ID)
				if err != nil {
					t.Error(err)
					return
				}
				latest.Content = "new dog"
				// Deliberately keep UpdatedAt: the source digest must catch this.
				if _, err := store.Save(latest); err != nil {
					t.Error(err)
				}
			}}
			archive := New(store)
			if err := archive.Configure(provider, "current", 3); err != nil {
				t.Fatal(err)
			}
			if _, err := archive.Search(t.Context(), sessionstore.SearchOptions{Text: "canine"}); err != nil {
				t.Fatal(err)
			}
			latest, err := store.Get(record.ID)
			if deletion {
				if !errors.Is(err, sessionstore.ErrNotFound) {
					t.Fatalf("deleted record resurrected: %#v, %v", latest, err)
				}
			} else if err != nil || latest.Content != "new dog" || latest.Embedding == nil || latest.Embedding.Vector[1] != 1 || !latest.UpdatedAt.Equal(record.UpdatedAt) {
				t.Fatalf("source change was overwritten or not re-embedded: %#v, %v", latest, err)
			}
		})
	}
}

func (e *repairEmbedder) Embed(ctx context.Context, input client.EmbeddingRequest) (*client.EmbeddingResponse, error) {
	if e.fail {
		return nil, errors.New("provider unavailable")
	}
	return e.testEmbedder.Embed(ctx, input)
}

func TestSearchRepairsUnregisteredWorkspaceAndLaterImports(t *testing.T) {
	// This archive has no Studio registration or explicit synchronization call.
	store, err := sessionstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	old, err := store.Save(sessionstore.Record{
		Kind: sessionstore.KindMessage, Content: "a cat naps",
		Embedding: &sessionstore.Embedding{Model: "previous", Dimensions: 2, Vector: []float32{0, 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	archive := New(store)
	embedder := &repairEmbedder{}
	if err := archive.Configure(embedder, "current", 3); err != nil {
		t.Fatal(err)
	}
	search := func() {
		t.Helper()
		result, err := archive.Search(t.Context(), sessionstore.SearchOptions{Text: "feline", Limit: 10})
		if err != nil || len(result.Hits) == 0 || result.Hits[0].VectorScore == 0 {
			t.Fatalf("automatic semantic repair = %#v, %v", result, err)
		}
	}
	search()
	updated, err := store.Get(old.ID)
	if err != nil || updated.Embedding.Model != "current" || updated.Embedding.Dimensions != 3 ||
		updated.Content != old.Content || !updated.UpdatedAt.Equal(old.UpdatedAt) {
		t.Fatalf("repaired record = %#v, %v", updated, err)
	}
	calls := len(embedder.calls)
	search()
	if len(embedder.calls) != calls+1 {
		t.Fatal("matching records were embedded again")
	}
	// A graph drift must be repaired even when the records already match.
	if err := store.ConfigureVector(sessionstore.VectorConfig{Model: "previous", Dimensions: 2}); err != nil {
		t.Fatal(err)
	}
	search()
	if config := store.VectorConfig(); config.Model != "current" || config.Dimensions != 3 {
		t.Fatalf("derived graph was not repaired = %#v", config)
	}
	imported, err := store.Save(sessionstore.Record{Kind: sessionstore.KindMessage, Content: "another cat"})
	if err != nil {
		t.Fatal(err)
	}
	search()
	updated, err = store.Get(imported.ID)
	if err != nil || updated.Embedding == nil || updated.Embedding.Model != "current" {
		t.Fatalf("late import was not repaired = %#v, %v", updated, err)
	}
	if err := archive.Configure(embedder, "next", 3); err != nil {
		t.Fatal(err)
	}
	search()
	for _, id := range []string{old.ID, imported.ID} {
		updated, err := store.Get(id)
		if err != nil || updated.Embedding.Model != "next" {
			t.Fatalf("model change was not repaired = %#v, %v", updated, err)
		}
	}
}

func TestSearchReportsRepairFailureAndRetriesOnUse(t *testing.T) {
	store, err := sessionstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	record, err := store.Save(sessionstore.Record{Kind: sessionstore.KindMessage, Content: "cat"})
	if err != nil {
		t.Fatal(err)
	}
	archive := New(store)
	embedder := &repairEmbedder{fail: true}
	if err := archive.Configure(embedder, "current", 3); err != nil {
		t.Fatal(err)
	}
	options := sessionstore.SearchOptions{Text: "cat", Limit: 1}
	if result, err := archive.Search(t.Context(), options); err == nil || len(result.Hits) != 0 {
		t.Fatalf("failed repair returned a partial search = %#v, %v", result, err)
	}
	// Chronological reads still work while the provider is unavailable.
	chronological := options
	chronological.Sort = sessionstore.SortOldest
	if _, err := archive.Search(t.Context(), chronological); err != nil {
		t.Fatal(err)
	}
	embedder.fail = false
	if _, err := archive.Search(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Get(record.ID)
	if err != nil || updated.Embedding == nil || updated.Embedding.Model != "current" {
		t.Fatalf("retry did not repair record = %#v, %v", updated, err)
	}
}

type blockingEmbedder struct {
	testEmbedder
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *blockingEmbedder) Embed(ctx context.Context, input client.EmbeddingRequest) (*client.EmbeddingResponse, error) {
	e.once.Do(func() { close(e.started) })
	select {
	case <-e.release:
		return e.testEmbedder.Embed(ctx, input)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestConcurrentSearchWaitCanCancelAndDoesNotDuplicateRepair(t *testing.T) {
	store, err := sessionstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Save(sessionstore.Record{Kind: sessionstore.KindMessage, Content: "cat"}); err != nil {
		t.Fatal(err)
	}
	embedder := &blockingEmbedder{started: make(chan struct{}), release: make(chan struct{})}
	archive := New(store)
	if err := archive.Configure(embedder, "current", 3); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := archive.Search(ctx, sessionstore.SearchOptions{Text: "feline"})
		done <- err
	}()
	select {
	case <-embedder.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer waitCancel()
	if _, err := archive.Search(waitCtx, sessionstore.SearchOptions{Text: "feline"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting search error = %v", err)
	}
	close(embedder.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Search(ctx, sessionstore.SearchOptions{Text: "feline"}); err != nil {
		t.Fatal(err)
	}
	if len(embedder.calls) != 3 { // one record repair and two successful queries
		t.Fatalf("duplicate record repair: %#v", embedder.calls)
	}
}
