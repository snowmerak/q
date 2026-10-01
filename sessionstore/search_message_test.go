package sessionstore

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestSearchOmitsOnlyCallingAssistantBeforePagination(t *testing.T) {
	for _, taskID := range []string{"", "child"} {
		t.Run("agent="+taskID, func(t *testing.T) {
			store, err := OpenWithOptions(t.TempDir(), OpenOptions{Vector: testVectorConfig()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			created := time.Now().UTC().Add(-time.Hour)
			message := func(callID string) json.RawMessage {
				payload := `{"role":"assistant","tool_calls":[{"id":"` + callID + `"}]}`
				if taskID == "" {
					payload = `{"conversation_id":"conversation","message":` + payload + `}`
				}
				return json.RawMessage(payload)
			}
			records := []Record{
				{ID: "older-identical", RunID: "run", TaskID: taskID, Payload: message("previous-search")},
				{ID: "other-session", RunID: "other", TaskID: taskID, Payload: message("search")},
				{ID: "other-agent", RunID: "run", TaskID: taskID + "-sibling", Payload: message("search")},
				{ID: "user", RunID: "run", TaskID: taskID, Payload: json.RawMessage(`{"role":"user","tool_calls":[{"id":"search"}]}`)},
				{ID: "result", Kind: KindResult, RunID: "run", TaskID: taskID, Payload: message("search")},
				{ID: "current", RunID: "run", TaskID: taskID, Payload: message("search")},
			}
			for index, record := range records {
				if record.Kind == "" {
					record.Kind = KindMessage
				}
				record.Content = "archive repeated response"
				record.CreatedAt = created.Add(time.Duration(index) * time.Minute)
				record.Embedding = &Embedding{Model: "embed-test", Vector: []float32{1, 0, 0}}
				if _, err := store.Save(record); err != nil {
					t.Fatal(err)
				}
			}
			for name, options := range map[string]SearchOptions{
				"text":    {Text: "archive"},
				"newest":  {Sort: SortNewest},
				"oldest":  {Sort: SortOldest},
				"recency": {Text: "archive", Recency: &Recency{Weight: 1, HalfLife: time.Hour}},
				"vector":  {Vector: &VectorQuery{Embedding: []float32{1, 0, 0}}},
				"hybrid":  {Text: "archive", Vector: &VectorQuery{Embedding: []float32{1, 0, 0}}},
			} {
				t.Run(name, func(t *testing.T) {
					options.Limit = 50
					unfiltered, err := store.Search(t.Context(), options)
					if err != nil {
						t.Fatal(err)
					}
					var want []string
					for _, hit := range unfiltered.Hits {
						if hit.Record.ID != "current" {
							want = append(want, hit.Record.ID)
						}
					}
					if len(want) != len(records)-1 {
						t.Fatalf("unfiltered search = %#v", unfiltered)
					}
					options.ExcludeMessage = &MessageToolCall{RunID: "run", TaskID: taskID, ToolCallID: "search"}
					filtered, err := store.Search(t.Context(), options)
					if err != nil || filtered.Total != uint64(len(want)) {
						t.Fatalf("filtered search = %#v, error = %v", filtered, err)
					}
					options.Limit = 2
					var got []string
					for options.Offset = 0; options.Offset < len(records); options.Offset += options.Limit {
						page, err := store.Search(t.Context(), options)
						if err != nil {
							t.Fatal(err)
						}
						for _, hit := range page.Hits {
							got = append(got, hit.Record.ID)
						}
						if options.Offset == 0 && len(page.Hits) != options.Limit {
							t.Fatalf("first page was not refilled: %#v", page)
						}
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("paginated IDs = %v, want %v", got, want)
					}
					if _, err := store.Get("current"); err != nil {
						t.Fatalf("excluded record was removed from storage: %v", err)
					}
				})
			}
		})
	}
}

func TestSearchMessageExclusionRefillsMinimalCandidateWindow(t *testing.T) {
	store, err := OpenWithOptions(t.TempDir(), OpenOptions{Vector: testVectorConfig()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for index, id := range []string{"past", "current"} {
		_, err := store.Save(Record{
			ID: id, Kind: KindMessage, RunID: "run", Content: "archive",
			CreatedAt: time.Now().UTC().Add(time.Duration(index-2) * time.Minute),
			Payload:   json.RawMessage(`{"role":"assistant","tool_calls":[{"id":"` + id + `"}]}`),
			Embedding: &Embedding{Model: "embed-test", Vector: []float32{1, 0, 0}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for name, options := range map[string]SearchOptions{
		"recency": {Text: "archive", Recency: &Recency{Weight: 1, HalfLife: time.Hour, CandidateLimit: 1}},
		"vector":  {Vector: &VectorQuery{Embedding: []float32{1, 0, 0}, CandidateLimit: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			options.Limit = 1
			options.ExcludeMessage = &MessageToolCall{RunID: "run", ToolCallID: "current"}
			result, err := store.Search(t.Context(), options)
			if err != nil || len(result.Hits) != 1 || result.Hits[0].Record.ID != "past" {
				t.Fatalf("refilled search = %#v, error = %v", result, err)
			}
		})
	}
}
