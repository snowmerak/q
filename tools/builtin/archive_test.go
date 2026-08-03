package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/sessionstore"
)

func TestSearchArchiveFiltersAndBoundsResults(t *testing.T) {
	store, err := sessionstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	if _, err := store.Save(sessionstore.Record{
		ID: "old", Kind: sessionstore.KindMessage, RunID: "run-1", Role: "coder",
		Status: sessionstore.StatusSucceeded, CreatedAt: created, UpdatedAt: created,
		Summary: "archive implementation", Content: "implemented durable history with bounded excerpts",
	}); err != nil {
		t.Fatal(err)
	}
	longContent := "archive result " + strings.Repeat("가", archiveSearchExcerptRunes+20)
	if _, err := store.Save(sessionstore.Record{
		ID: "new", Kind: sessionstore.KindResult, RunID: "run-1", Role: "coder",
		Status: sessionstore.StatusSucceeded, CreatedAt: created.Add(time.Hour), UpdatedAt: created.Add(time.Hour),
		Summary: "archive implementation result", Content: longContent,
	}); err != nil {
		t.Fatal(err)
	}

	output, err := SearchArchive(context.Background(), store, SearchArchiveInput{
		Query: "archive implementation", Kinds: []string{sessionstore.KindResult},
		Roles: []string{"coder"}, Statuses: []string{sessionstore.StatusSucceeded}, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Total != 1 || len(output.Hits) != 1 || output.Hits[0].ID != "new" {
		t.Fatalf("search output = %#v", output)
	}
	if !output.Hits[0].Truncated || len([]rune(output.Hits[0].Excerpt)) != archiveSearchExcerptRunes {
		t.Fatalf("bounded excerpt = %#v", output.Hits[0])
	}

	latest, err := SearchArchive(context.Background(), store, SearchArchiveInput{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(latest.Hits) != 1 || latest.Hits[0].ID != "new" {
		t.Fatalf("default newest output = %#v", latest)
	}
}

func TestGetArchiveRecordPaginatesContentAndIncludesBoundedPayload(t *testing.T) {
	store, err := sessionstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	payload := json.RawMessage(`{"choice":"approved"}`)
	if _, err := store.Save(sessionstore.Record{
		ID: "record-1", Kind: sessionstore.KindResult, Content: "가나다라마바사", Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}

	output, err := GetArchiveRecord(store, GetArchiveRecordInput{
		ID: "record-1", ContentOffset: 2, ContentLimit: 3, IncludePayload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Content != "다라마" || output.NextContentOffset != 5 || !output.MoreContent {
		t.Fatalf("paginated record = %#v", output)
	}
	payloadObject, ok := output.Payload.(map[string]any)
	if !ok || payloadObject["choice"] != "approved" || output.PayloadBytes == 0 {
		t.Fatalf("payload = %#v, bytes = %d", output.Payload, output.PayloadBytes)
	}
}

func TestArchiveToolsRejectInvalidBounds(t *testing.T) {
	store, err := sessionstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := SearchArchive(context.Background(), store, SearchArchiveInput{CreatedAfter: "yesterday"}); err == nil {
		t.Fatal("SearchArchive accepted a non-RFC3339 date")
	}
	if _, err := SearchArchive(context.Background(), store, SearchArchiveInput{Limit: maximumArchiveSearchLimit + 1}); err == nil {
		t.Fatal("SearchArchive accepted an excessive limit")
	}
	if _, err := GetArchiveRecord(store, GetArchiveRecordInput{ID: "missing", ContentLimit: maximumRecordContentRunes + 1}); err == nil {
		t.Fatal("GetArchiveRecord accepted an excessive content limit")
	}
}
