package sessionstore

import (
	"context"
	"errors"
	"testing"
)

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
