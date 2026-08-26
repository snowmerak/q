package sessionstore

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type interfaceWriterStore struct {
	mu      sync.Mutex
	records []Record
	closed  int
}

func (s *interfaceWriterStore) Save(record Record) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, cloneRecord(record))
	return cloneRecord(record), nil
}

func (s *interfaceWriterStore) SaveBatch(records []Record) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, cloneRecordSlice(records)...)
	return cloneRecordSlice(records), nil
}

func (s *interfaceWriterStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed++
	return nil
}

func TestWriterUsesInterfaceBackedStore(t *testing.T) {
	store := &interfaceWriterStore{}
	writer := NewWriterWithOptions(store, WriterOptions{Buffer: 8, BatchSize: 8})
	for _, content := range []string{"first remote record", "second remote record"} {
		if err := writer.Append(Record{Kind: KindMessage, Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.records) != 2 || store.records[0].Content != "first remote record" ||
		store.records[1].Content != "second remote record" {
		t.Fatalf("records = %#v", store.records)
	}
	if store.closed != 1 {
		t.Fatalf("close calls = %d, want 1", store.closed)
	}
}

type indexingFailureWriterStore struct {
	retried []Record
}

func (s *indexingFailureWriterStore) Save(record Record) (Record, error) {
	s.retried = append(s.retried, cloneRecord(record))
	return record, nil
}

func (s *indexingFailureWriterStore) SaveBatch(records []Record) ([]Record, error) {
	prepared := cloneRecordSlice(records)
	prepared[0].ID = "server-assigned-id"
	return prepared, &IndexingError{RecordID: prepared[0].ID, Err: errors.New("index unavailable")}
}

func (*indexingFailureWriterStore) Close() error { return nil }

func TestWriterFallbackReusesStoreAssignedRecordID(t *testing.T) {
	store := &indexingFailureWriterStore{}
	writer := NewWriterWithOptions(store, WriterOptions{Buffer: 1, BatchSize: 1})
	if err := writer.Append(Record{Kind: KindMessage, Content: "durable before indexing"}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err == nil {
		t.Fatal("Flush succeeded after indexing failure")
	}
	if len(store.retried) != 1 || store.retried[0].ID != "server-assigned-id" {
		t.Fatalf("fallback records = %#v", store.retried)
	}
	_ = writer.Close()
}

func TestWriterAppendsAndFlushesRecordsInOrder(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writer := NewWriter(store, 8)
	t.Cleanup(func() { _ = writer.Close() })

	for _, content := range []string{"first archive record", "second archive record"} {
		if err := writer.Append(Record{Kind: KindMessage, RunID: "run-1", Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	results, err := store.Search(context.Background(), SearchOptions{Text: "archive record", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results.Hits) != 2 {
		t.Fatalf("hits = %#v", results.Hits)
	}
}

func TestWriterPersistsRecordsWhenOptionalPreparationFails(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writer := NewWriterWithOptions(store, WriterOptions{
		Buffer: 8,
		Prepare: func(context.Context, []Record) ([]Record, error) {
			return nil, errors.New("embedding service unavailable")
		},
	})
	t.Cleanup(func() { _ = writer.Close() })
	if err := writer.Append(Record{Kind: KindMessage, Content: "lexical fallback survives"}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err == nil {
		t.Fatal("Flush succeeded after preparation failure")
	}
	result, err := store.Search(context.Background(), SearchOptions{Text: "lexical fallback", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 {
		t.Fatalf("fallback hits = %#v", result.Hits)
	}
}
