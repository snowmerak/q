package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type captureUsageRecorder struct {
	mu      sync.Mutex
	records []UsageRecord
	err     error
}

func (r *captureUsageRecorder) RecordUsage(record UsageRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, record)
	return r.err
}

func (r *captureUsageRecorder) snapshot() []UsageRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]UsageRecord(nil), r.records...)
}

func TestChatRecordsRemoteTokenUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{
			"id":"chat_usage","model":"provider-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150,
				"prompt_tokens_details":{"cached_tokens":80,"cache_write_tokens":12}}
		}`)
	}))
	defer server.Close()

	recorder := &captureUsageRecorder{}
	configured, err := New(Config{
		BaseURL: server.URL, DefaultModel: "request-model", DisableAPIKey: true, UsageRecorder: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer configured.Close()
	if _, err := configured.Chat(t.Context(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}); err != nil {
		t.Fatal(err)
	}

	records := recorder.snapshot()
	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	record := records[0]
	if record.Model != "provider-model" || record.PromptTokens != 120 || record.CompletionTokens != 30 ||
		record.TotalTokens != 150 || record.CachedTokens != 80 || record.CacheWriteTokens != 12 {
		t.Fatalf("record = %#v", record)
	}
	if record.Estimated || record.CacheEstimated || record.At.IsZero() {
		t.Fatalf("record flags/time = %#v", record)
	}
	if len(record.EventID) != 32 || record.Role != UsageRoleUnknown {
		t.Fatalf("record identity/role = %#v", record)
	}
}

func TestChatRecordsBoundedUsageRoleFromContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"choices":[{"index":0,"message":{"role":"assistant","content":"done"}}],"usage":{"total_tokens":4}}`)
	}))
	defer server.Close()
	recorder := &captureUsageRecorder{}
	configured, err := New(Config{BaseURL: server.URL, DisableAPIKey: true, UsageRecorder: recorder})
	if err != nil {
		t.Fatal(err)
	}
	defer configured.Close()
	if _, err := configured.Chat(WithUsageRole(t.Context(), "Planner"), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if role := recorder.snapshot()[0].Role; role != "planner" {
		t.Fatalf("role = %q", role)
	}
	if _, err := configured.Chat(WithUsageRole(t.Context(), "user supplied role!"), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if role := recorder.snapshot()[1].Role; role != UsageRoleUnknown {
		t.Fatalf("invalid role = %q", role)
	}
}

func TestNormalizedUsageDerivesMissingRemoteTotalsWithoutEstimating(t *testing.T) {
	record := normalizedUsageRecord("model", Usage{PromptTokens: 9, CompletionTokens: 2}, 100, 100)
	if record.PromptTokens != 9 || record.CompletionTokens != 2 || record.TotalTokens != 11 || record.Estimated {
		t.Fatalf("record = %#v", record)
	}

	record = normalizedUsageRecord("model", Usage{CompletionTokens: 2, TotalTokens: 11}, 100, 100)
	if record.PromptTokens != 9 || record.CompletionTokens != 2 || record.TotalTokens != 11 || record.Estimated {
		t.Fatalf("derived record = %#v", record)
	}
}

func TestChatEstimatesMissingUsageAndIgnoresRecorderFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{
			"id":"chat_estimated",
			"choices":[{"index":0,"message":{"role":"assistant","content":"estimated response"},"finish_reason":"stop"}]
		}`)
	}))
	defer server.Close()

	recorder := &captureUsageRecorder{err: errors.New("disk unavailable")}
	configured, err := New(Config{
		BaseURL: server.URL, DefaultModel: "local-model", DisableAPIKey: true, UsageRecorder: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer configured.Close()
	response, err := configured.Chat(t.Context(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "estimate this request"}},
	})
	if err != nil || response.ID != "chat_estimated" {
		t.Fatalf("response = %#v, err = %v", response, err)
	}

	records := recorder.snapshot()
	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	record := records[0]
	if record.Model != "local-model" || record.PromptTokens <= 0 || record.CompletionTokens <= 0 ||
		record.TotalTokens != record.PromptTokens+record.CompletionTokens {
		t.Fatalf("estimated record = %#v", record)
	}
	if !record.Estimated || !record.CacheEstimated || record.CachedTokens != 0 || record.CacheWriteTokens != 0 {
		t.Fatalf("estimated flags/cache = %#v", record)
	}
}

func TestChatDoesNotRecordRejectedCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(writer, `{"error":{"message":"slow down"}}`)
	}))
	defer server.Close()

	recorder := &captureUsageRecorder{}
	configured, err := New(Config{BaseURL: server.URL, DisableAPIKey: true, UsageRecorder: recorder})
	if err != nil {
		t.Fatal(err)
	}
	defer configured.Close()
	if _, err := configured.Chat(t.Context(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}); err == nil {
		t.Fatal("Chat unexpectedly succeeded")
	}
	if records := recorder.snapshot(); len(records) != 0 {
		t.Fatalf("records = %#v", records)
	}
}

func TestChatStreamRecordsUsageExactlyOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("X-Trace-Test", "preserved")
		_, _ = io.WriteString(writer, "data: {\"id\":\"chat_stream\",\"model\":\"stream-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"}}]}\n\n")
		_, _ = io.WriteString(writer, "data: {\"id\":\"chat_stream\",\"model\":\"stream-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":5,\"total_tokens\":25,\"prompt_tokens_details\":{\"cached_tokens\":9,\"cache_write_tokens\":3}}}\n\n")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	recorder := &captureUsageRecorder{}
	configured, err := New(Config{
		BaseURL: server.URL, DefaultModel: "request-model", DisableAPIKey: true, UsageRecorder: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer configured.Close()
	stream, err := configured.ChatStream(t.Context(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	headerer, ok := stream.(ResponseHeaderer)
	if !ok || headerer.ResponseHeaders().Get("X-Trace-Test") != "preserved" {
		t.Fatalf("stream headers were not preserved: %T", stream)
	}
	for {
		_, err = stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}

	records := recorder.snapshot()
	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	record := records[0]
	if record.Model != "stream-model" || record.PromptTokens != 20 || record.CompletionTokens != 5 ||
		record.TotalTokens != 25 || record.CachedTokens != 9 || record.CacheWriteTokens != 3 ||
		record.Estimated || record.CacheEstimated {
		t.Fatalf("record = %#v", record)
	}
}

func TestChatStreamRecordsAnEmptyProviderResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	recorder := &captureUsageRecorder{}
	configured, err := New(Config{
		BaseURL: server.URL, DefaultModel: "empty-stream-model", DisableAPIKey: true, UsageRecorder: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer configured.Close()
	stream, err := configured.ChatStream(t.Context(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("stream final error = %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}

	records := recorder.snapshot()
	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	record := records[0]
	if record.Model != "empty-stream-model" || record.PromptTokens <= 0 || record.CompletionTokens != 0 ||
		record.TotalTokens != record.PromptTokens || !record.Estimated || !record.CacheEstimated {
		t.Fatalf("record = %#v", record)
	}
}

func TestEmbedRecordsReportedAndEstimatedUsage(t *testing.T) {
	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		call++
		writer.Header().Set("Content-Type", "application/json")
		if call == 1 {
			_, _ = io.WriteString(writer, `{
				"object":"list","model":"embedding-provider-model",
				"data":[{"object":"embedding","embedding":[0.1],"index":0}],
				"usage":{"prompt_tokens":7,"total_tokens":7}
			}`)
			return
		}
		_, _ = io.WriteString(writer, `{
			"object":"list","data":[{"object":"embedding","embedding":[0.2],"index":0}]
		}`)
	}))
	defer server.Close()

	recorder := &captureUsageRecorder{}
	configured, err := New(Config{
		BaseURL: server.URL, DefaultModel: "embedding-request-model", DisableAPIKey: true, UsageRecorder: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer configured.Close()
	if _, err := configured.Embed(context.Background(), EmbeddingRequest{Input: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := configured.Embed(context.Background(), EmbeddingRequest{Input: []string{"second", "third"}}); err != nil {
		t.Fatal(err)
	}

	records := recorder.snapshot()
	if len(records) != 2 {
		t.Fatalf("records = %#v", records)
	}
	if records[0].Model != "embedding-provider-model" || records[0].PromptTokens != 7 ||
		records[0].CompletionTokens != 0 || records[0].TotalTokens != 7 || records[0].Estimated {
		t.Fatalf("reported embedding record = %#v", records[0])
	}
	if records[1].Model != "embedding-request-model" || records[1].PromptTokens <= 0 ||
		records[1].CompletionTokens != 0 || records[1].TotalTokens != records[1].PromptTokens ||
		!records[1].Estimated {
		t.Fatalf("estimated embedding record = %#v", records[1])
	}
	for _, record := range records {
		if !record.CacheEstimated || record.CachedTokens != 0 || record.CacheWriteTokens != 0 {
			t.Fatalf("embedding cache record = %#v", record)
		}
	}
}
