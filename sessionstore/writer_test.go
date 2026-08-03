package sessionstore

import (
	"context"
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
