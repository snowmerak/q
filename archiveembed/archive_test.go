package archiveembed

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/sessionstore"
)

type testEmbedder struct {
	mu    sync.Mutex
	calls [][]string
}

func (e *testEmbedder) Embed(_ context.Context, request client.EmbeddingRequest) (*client.EmbeddingResponse, error) {
	texts, ok := request.Input.([]string)
	if !ok {
		return nil, &testError{"input is not a string batch"}
	}
	e.mu.Lock()
	e.calls = append(e.calls, append([]string(nil), texts...))
	e.mu.Unlock()
	data := make([]client.Embedding, len(texts))
	for index, text := range texts {
		vector := []float32{0, 0, 1}
		switch {
		case strings.Contains(text, "cat"), strings.Contains(text, "feline"):
			vector = []float32{1, 0, 0}
		case strings.Contains(text, "dog"), strings.Contains(text, "canine"):
			vector = []float32{0, 1, 0}
		}
		data[index] = client.Embedding{Index: index, Embedding: vector}
	}
	return &client.EmbeddingResponse{Data: data}, nil
}

type testError struct{ message string }

func (e *testError) Error() string { return e.message }

type interfaceArchiveStore struct {
	configured sessionstore.VectorConfig
	records    map[string]sessionstore.Record
	search     sessionstore.SearchResult
	options    sessionstore.SearchOptions
}

func (s *interfaceArchiveStore) ConfigureVector(config sessionstore.VectorConfig) error {
	s.configured = config
	return nil
}

func (s *interfaceArchiveStore) Save(record sessionstore.Record) (sessionstore.Record, error) {
	if s.records == nil {
		s.records = make(map[string]sessionstore.Record)
	}
	s.records[record.ID] = record
	return record, nil
}

func (s *interfaceArchiveStore) SaveBatch(records []sessionstore.Record) ([]sessionstore.Record, error) {
	for _, record := range records {
		if _, err := s.Save(record); err != nil {
			return nil, err
		}
	}
	return records, nil
}

func (s *interfaceArchiveStore) Get(id string) (sessionstore.Record, error) {
	record, ok := s.records[id]
	if !ok {
		return sessionstore.Record{}, sessionstore.ErrNotFound
	}
	return record, nil
}

func (s *interfaceArchiveStore) Delete(id string) error {
	delete(s.records, id)
	return nil
}

func (s *interfaceArchiveStore) Search(_ context.Context, options sessionstore.SearchOptions) (sessionstore.SearchResult, error) {
	s.options = options
	return s.search, nil
}

func TestArchiveUsesInterfaceBackedStore(t *testing.T) {
	store := &interfaceArchiveStore{records: make(map[string]sessionstore.Record)}
	archive := New(store)
	embedder := &testEmbedder{}
	if err := archive.Configure(embedder, "embed-test", 3); err != nil {
		t.Fatal(err)
	}
	if store.configured.Model != "embed-test" || store.configured.Dimensions != 3 {
		t.Fatalf("vector config = %#v", store.configured)
	}

	record := sessionstore.Record{ID: "remote-record", Kind: sessionstore.KindMessage, Content: "cat memory"}
	if _, err := archive.Save(record); err != nil {
		t.Fatal(err)
	}
	if stored, err := archive.Get(record.ID); err != nil || stored.Content != record.Content {
		t.Fatalf("Get() = (%#v, %v)", stored, err)
	}
	if _, err := archive.Search(context.Background(), sessionstore.SearchOptions{
		Text: "feline", Filters: sessionstore.Filters{Kinds: []string{sessionstore.KindMessage}},
	}); err != nil {
		t.Fatal(err)
	}
	if store.options.Vector == nil || len(store.options.Vector.Embedding) != 3 {
		t.Fatalf("semantic search options = %#v", store.options)
	}
	if err := archive.Delete(record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Get(record.ID); !errors.Is(err, sessionstore.ErrNotFound) {
		t.Fatalf("Get() error after delete = %v", err)
	}
}

func TestBackfillAndSemanticSearch(t *testing.T) {
	store, err := sessionstore.OpenWithOptions(t.TempDir(), sessionstore.OpenOptions{
		Vector: sessionstore.VectorConfig{Model: "embed-test", Dimensions: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cat, err := store.Save(sessionstore.Record{Kind: sessionstore.KindMessage, Content: "a cat naps on the keyboard"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(sessionstore.Record{Kind: sessionstore.KindMessage, Content: "a dog waits by the door"}); err != nil {
		t.Fatal(err)
	}

	archive := New(store)
	embedder := &testEmbedder{}
	if err := archive.Configure(embedder, "embed-test", 3); err != nil {
		t.Fatal(err)
	}
	stats, err := archive.Backfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Scanned != 2 || stats.Embedded != 2 {
		t.Fatalf("backfill stats = %#v", stats)
	}
	stored, err := store.Get(cat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Embedding == nil || stored.Embedding.Model != "embed-test" || len(stored.Embedding.Vector) != 3 {
		t.Fatalf("stored embedding = %#v", stored.Embedding)
	}
	result, err := archive.Search(context.Background(), sessionstore.SearchOptions{
		Text: "feline", Filters: sessionstore.Filters{Kinds: []string{sessionstore.KindMessage}},
		Sort: sessionstore.SortRelevance, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || result.Hits[0].Record.ID != cat.ID || result.Hits[0].VectorScore == 0 {
		t.Fatalf("semantic hits = %#v", result.Hits)
	}
}

func TestPrepareSkipsArchiveReadsAndNonHistoryRecords(t *testing.T) {
	store, err := sessionstore.OpenWithOptions(t.TempDir(), sessionstore.OpenOptions{
		Vector: sessionstore.VectorConfig{Model: "embed-test", Dimensions: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	archive := New(store)
	embedder := &testEmbedder{}
	if err := archive.Configure(embedder, "embed-test", 3); err != nil {
		t.Fatal(err)
	}
	records, err := archive.Prepare(context.Background(), []sessionstore.Record{
		{Kind: sessionstore.KindMessage, Content: "cat memory"},
		{Kind: sessionstore.KindResult, Summary: "search_archive", Tags: []string{"archive-read"}},
		{Kind: sessionstore.KindSkill, Content: "cat skill"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Embedding == nil || records[1].Embedding != nil || records[2].Embedding != nil {
		t.Fatalf("prepared records = %#v", records)
	}
	embedder.mu.Lock()
	defer embedder.mu.Unlock()
	if len(embedder.calls) != 1 || len(embedder.calls[0]) != 1 {
		t.Fatalf("embedding calls = %#v", embedder.calls)
	}
}
