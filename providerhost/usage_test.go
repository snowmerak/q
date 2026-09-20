package providerhost

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/snowmerak/q/client"
)

type gatewayUsageRecorder struct {
	mu      sync.Mutex
	records []client.UsageRecord
	err     error
}

func (r *gatewayUsageRecorder) RecordUsage(record client.UsageRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, record)
	return r.err
}

func (r *gatewayUsageRecorder) snapshot() []client.UsageRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]client.UsageRecord(nil), r.records...)
}

func TestUsageTrackingHandlerRecordsJSONAndConsumesMetadata(t *testing.T) {
	eventID := strings.Repeat("a", 32)
	for _, test := range []struct {
		name       string
		path       string
		body       string
		model      string
		prompt     int
		completion int
		total      int
		cached     int
		written    int
	}{
		{
			name: "chat", path: "/v1/chat/completions", model: "provider/chat",
			body:   `{"model":"provider/chat","usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":7,"cache_write_tokens":2}}}`,
			prompt: 12, completion: 3, total: 15, cached: 7, written: 2,
		},
		{
			name: "responses", path: "/v1/responses", model: "provider/responses",
			body:   `{"type":"response.completed","response":{"model":"provider/responses","usage":{"input_tokens":20,"output_tokens":5,"total_tokens":25,"input_tokens_details":{"cached_tokens":9}}}}`,
			prompt: 20, completion: 5, total: 25, cached: 9,
		},
		{
			name: "embedding", path: "/v1/embeddings", model: "provider/embed",
			body:   `{"model":"provider/embed","usage":{"prompt_tokens":6,"total_tokens":6}}`,
			prompt: 6, total: 6,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &gatewayUsageRecorder{}
			inner := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Header.Get(client.UsageEventIDHeader) != "" || request.Header.Get(client.UsageRoleHeader) != "" {
					t.Fatalf("usage metadata reached provider handler: %#v", request.Header)
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, test.body)
			})
			handler := UsageTrackingHandler(recorder, inner)
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{}`))
			request.Header.Set(client.UsageEventIDHeader, eventID)
			request.Header.Set(client.UsageRoleHeader, "Planner")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != test.body {
				t.Fatalf("response status=%d body=%q", response.Code, response.Body.String())
			}
			records := recorder.snapshot()
			if len(records) != 1 {
				t.Fatalf("records = %#v", records)
			}
			record := records[0]
			if record.EventID != eventID || record.Role != "planner" || record.Model != test.model ||
				record.PromptTokens != test.prompt || record.CompletionTokens != test.completion ||
				record.TotalTokens != test.total || record.CachedTokens != test.cached ||
				record.CacheWriteTokens != test.written {
				t.Fatalf("record = %#v", record)
			}
			if record.CacheEstimated != (test.name == "embedding") {
				t.Fatalf("cache estimate = %#v", record)
			}
		})
	}
}

func TestUsageTrackingHandlerParsesSplitSSEAndDefaultsMetadata(t *testing.T) {
	recorder := &gatewayUsageRecorder{}
	inner := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "event: response.created\n")
		_, _ = io.WriteString(writer, `data: {"response":{"model":"stream/model"}}`)
		_, _ = io.WriteString(writer, "\n\n")
		_, _ = io.WriteString(writer, "event: response.completed\n")
		_, _ = io.WriteString(writer, `data: {"response":{"model":"stream/model","usage":{"input_tokens":30,"output_tokens":4,"total_tokens":34}}}`)
		_, _ = io.WriteString(writer, "\n\n")
		writer.(http.Flusher).Flush()
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	request.Header.Set(client.UsageEventIDHeader, "INVALID")
	response := httptest.NewRecorder()
	UsageTrackingHandler(recorder, inner).ServeHTTP(response, request)

	if !response.Flushed || !strings.Contains(response.Body.String(), "response.completed") {
		t.Fatalf("stream response was not preserved: flushed=%v body=%q", response.Flushed, response.Body.String())
	}
	records := recorder.snapshot()
	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	record := records[0]
	if !client.ValidUsageEventID(record.EventID) || record.EventID == "INVALID" ||
		record.Role != client.UsageRoleGateway || record.Model != "stream/model" ||
		record.PromptTokens != 30 || record.CompletionTokens != 4 || record.TotalTokens != 34 {
		t.Fatalf("record = %#v", record)
	}
}

func TestUsageTrackingHandlerParsesGatewayChatSSEWrites(t *testing.T) {
	recorder := &gatewayUsageRecorder{}
	inner := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: ")
		_, _ = io.WriteString(writer, `{"model":"chat/stream","choices":[{"index":0,"delta":{"content":"ok"}}]}`)
		_, _ = io.WriteString(writer, "\n\n")
		_, _ = io.WriteString(writer, "data: ")
		_, _ = io.WriteString(writer, `{"model":"chat/stream","choices":[],"usage":{"prompt_tokens":14,"completion_tokens":2,"total_tokens":16}}`)
		_, _ = io.WriteString(writer, "\n\n")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
		writer.(http.Flusher).Flush()
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	request.Header.Set(client.UsageEventIDHeader, strings.Repeat("b", 32))
	request.Header.Set(client.UsageRoleHeader, "executor")
	response := httptest.NewRecorder()
	UsageTrackingHandler(recorder, inner).ServeHTTP(response, request)

	records := recorder.snapshot()
	if len(records) != 1 || records[0].Model != "chat/stream" || records[0].Role != "executor" ||
		records[0].PromptTokens != 14 || records[0].CompletionTokens != 2 || records[0].TotalTokens != 16 {
		t.Fatalf("records = %#v", records)
	}
	if !response.Flushed || !strings.HasSuffix(response.Body.String(), "data: [DONE]\n\n") {
		t.Fatalf("chat stream response was not preserved: flushed=%v body=%q", response.Flushed, response.Body.String())
	}
}

func TestUsageTrackingHandlerKeepsResponsesIndependentFromRecording(t *testing.T) {
	recorder := &gatewayUsageRecorder{err: errors.New("usage unavailable")}
	success := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"model":"m","usage":{"total_tokens":3}}`)
	})
	response := httptest.NewRecorder()
	UsageTrackingHandler(recorder, success).ServeHTTP(response,
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if response.Code != http.StatusOK || response.Body.String() != `{"model":"m","usage":{"total_tokens":3}}` {
		t.Fatalf("successful response changed: status=%d body=%q", response.Code, response.Body.String())
	}
	if len(recorder.snapshot()) != 1 {
		t.Fatalf("recording was not attempted: %#v", recorder.snapshot())
	}

	failed := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(writer, `{"usage":{"total_tokens":99}}`)
	})
	response = httptest.NewRecorder()
	UsageTrackingHandler(recorder, failed).ServeHTTP(response,
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if response.Code != http.StatusBadGateway || len(recorder.snapshot()) != 1 {
		t.Fatalf("failed response status=%d records=%#v", response.Code, recorder.snapshot())
	}
}
