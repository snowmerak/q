package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/diagnostics"
)

func modelLogContext(t *testing.T) (context.Context, *bytes.Buffer) {
	t.Helper()
	output := &bytes.Buffer{}
	ctx := diagnostics.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(output, nil)))
	ctx = diagnostics.WithRequestID(ctx, "")
	ctx = diagnostics.WithAttrs(ctx, slog.String("session_id", "session-1"), slog.String("run_id", "run-1"), slog.String("agent", "worker"), slog.String("task_id", "task-1"))
	return WithUsageRole(ctx, "planner"), output
}

func modelLogRecords(t *testing.T, output *bytes.Buffer) []map[string]any {
	t.Helper()
	if strings.Contains(output.String(), "private-") {
		t.Fatalf("private payload in logs: %s", output.String())
	}
	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func assertModelLogPair(t *testing.T, ctx context.Context, records []map[string]any, model, mode, status string, streaming bool) {
	t.Helper()
	if len(records) != 2 || records[0]["msg"] != "model.call.started" || records[1]["msg"] != "model.call.finished" || records[0]["model_call_id"] == "" || records[0]["model_call_id"] != records[1]["model_call_id"] {
		t.Fatalf("call lifecycle = %+v", records)
	}
	for _, record := range records {
		if record["request_id"] != diagnostics.RequestID(ctx) || record["requested_model"] != model || record["api_mode"] != mode || record["streaming"] != streaming || record["usage_role"] != "planner" || record["session_id"] != "session-1" || record["run_id"] != "run-1" || record["agent"] != "worker" || record["task_id"] != "task-1" {
			t.Fatalf("call metadata = %+v", record)
		}
	}
	if records[1]["status"] != status || records[1]["duration_ms"] == nil {
		t.Fatalf("call result = %+v", records[1])
	}
}

func TestModelCallLogsActualModelAndUsageAtInfo(t *testing.T) {
	for _, mode := range []string{"chat_completions", "responses"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.Header().Set("X-Private", "private-response-header")
				if mode == "responses" {
					_, _ = io.WriteString(writer, `{"id":"resp_1","model":"resolved/model","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"private-response"}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`)
				} else {
					_, _ = io.WriteString(writer, `{"model":"resolved/model","choices":[{"index":0,"message":{"role":"assistant","content":"private-response"}}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
				}
			}))
			defer server.Close()
			configured, err := New(Config{BaseURL: server.URL, APIKey: "private-api-key", DefaultModel: "selected/model", ModelAPIModes: map[string]string{"selected/model": mode}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = configured.Close() })
			ctx, output := modelLogContext(t)
			response, err := configured.Chat(ctx, ChatRequest{Messages: []Message{{Role: RoleUser, Content: "private-prompt"}}, Headers: http.Header{"X-Private": {"private-request-header"}}})
			if err != nil || response == nil {
				t.Fatalf("chat = %+v, %v", response, err)
			}
			records := modelLogRecords(t, output)
			assertModelLogPair(t, ctx, records, "selected/model", mode, "completed", false)
			last := records[1]
			if last["response_model"] != "resolved/model" || last["usage_reported"] != true || last["prompt_tokens"] != float64(10) || last["completion_tokens"] != float64(2) || last["total_tokens"] != float64(12) || last["level"] != "INFO" {
				t.Fatalf("reported usage = %+v", last)
			}
		})
	}
}

func TestModelStreamLogsCompletionAfterEOFForBothAPIs(t *testing.T) {
	for _, mode := range []string{"chat_completions", "responses"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				writer.Header().Set("X-Trace-Test", "preserved")
				if mode == "responses" {
					_, _ = io.WriteString(writer, "event: response.reasoning_summary_text.delta\ndata: "+`{"type":"response.reasoning_summary_text.delta","delta":"private-reasoning"}`+"\n\n")
					_, _ = io.WriteString(writer, "event: response.output_text.delta\ndata: "+`{"type":"response.output_text.delta","delta":"private-response"}`+"\n\n")
					_, _ = io.WriteString(writer, "event: response.completed\ndata: "+`{"type":"response.completed","response":{"id":"resp_1","model":"resolved/model","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"private-response"}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`+"\n\n")
				} else {
					_, _ = io.WriteString(writer, "data: "+`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`+"\n\n")
					_, _ = io.WriteString(writer, "data: "+`{"choices":[{"index":0,"delta":{"reasoning_content":"private-reasoning"}}]}`+"\n\n")
					_, _ = io.WriteString(writer, "data: "+`{"model":"resolved/model","choices":[{"index":0,"delta":{"content":"private-response"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`+"\n\ndata: [DONE]\n\n")
				}
			}))
			defer server.Close()
			configured, err := New(Config{BaseURL: server.URL, APIKey: "private-api-key", DefaultModel: "selected/model", ModelAPIModes: map[string]string{"selected/model": mode}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = configured.Close() })
			ctx, output := modelLogContext(t)
			var timings []ModelCallTiming
			ctx = WithModelCallObserver(ctx, func(timing ModelCallTiming) {
				timings = append(timings, timing)
			})
			stream, err := configured.ChatStream(ctx, ChatRequest{Messages: []Message{{Role: RoleUser, Content: "private-prompt"}}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = stream.Close() })
			if records := modelLogRecords(t, output); len(records) != 1 {
				t.Fatalf("stream finished before consumption: %+v", records)
			}
			if headers, ok := stream.(ResponseHeaderer); !ok || headers.ResponseHeaders().Get("X-Trace-Test") != "preserved" {
				t.Fatal("stream lost response header interface")
			}
			for {
				chunk, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if chatChunkHasOutput(chunk) {
					// Keep a measurable first-output interval on low-resolution clocks.
					time.Sleep(20 * time.Millisecond)
				}
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
			records := modelLogRecords(t, output)
			assertModelLogPair(t, ctx, records, "selected/model", mode, "completed", true)
			if records[1]["response_model"] != "resolved/model" || records[1]["prompt_tokens"] != float64(10) || records[1]["total_tokens"] != float64(12) {
				t.Fatalf("stream metadata = %+v", records[1])
			}
			if len(timings) != 1 {
				t.Fatalf("observed calls = %+v", timings)
			}
			timing := timings[0]
			if timing.TTFTMS == nil || timing.AfterFirstMS == nil || timing.MaxOutputGapMS == nil || timing.OutputTokensPerSec == nil {
				t.Fatalf("incomplete stream timing = %+v", timing)
			}
			if last := records[1]; last["model_call_id"] != timing.ID || last["duration_ms"] != float64(timing.DurationMS) ||
				last["ttft_ms"] != float64(*timing.TTFTMS) || last["after_first_ms"] != float64(*timing.AfterFirstMS) ||
				last["max_output_gap_ms"] != float64(*timing.MaxOutputGapMS) || last["output_tokens_per_second"] != *timing.OutputTokensPerSec {
				t.Fatalf("log differs from observed timing: %+v, %+v", last, timing)
			}
		})
	}
}

func TestModelTimingUsesOutputDeltas(t *testing.T) {
	start := time.Now()
	call := &modelCallLog{started: start}
	for index, frame := range []string{
		`{"choices":[{"delta":{"role":"assistant"}}]}`,
		`{"choices":[],"usage":{"completion_tokens":20}}`,
		`{"choices":[{"delta":{"reasoning_content":"checking"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"lookup"}}]}}]}`,
	} {
		var chunk ChatChunk
		if err := json.Unmarshal([]byte(frame), &chunk); err != nil {
			t.Fatal(err)
		}
		chunk.Raw = []byte(frame)
		call.observeChunk(&chunk, start.Add(time.Duration(index)*time.Second))
	}
	tokens := 20
	timing := ModelCallTiming{Status: "completed", OutputTokens: &tokens}
	call.output.apply(&timing, start, start.Add(4*time.Second))
	if timing.TTFTMS == nil || *timing.TTFTMS != 2000 || timing.AfterFirstMS == nil || *timing.AfterFirstMS != 2000 ||
		timing.MaxOutputGapMS == nil || *timing.MaxOutputGapMS != 1000 || timing.OutputTokensPerSec == nil || *timing.OutputTokensPerSec != 10 {
		t.Fatalf("unexpected stream timing: %+v", timing)
	}
}

func TestModelFallbackLogsEachActualAttempt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body ChatRequest
		_ = json.NewDecoder(request.Body).Decode(&body)
		writer.Header().Set("Content-Type", "application/json")
		if body.Model == "primary" {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(writer, `{"error":{"message":"private-provider-error","type":"unavailable"}}`)
			return
		}
		_, _ = io.WriteString(writer, `{"model":"secondary","choices":[{"index":0,"message":{"role":"assistant","content":"private-response"}}]}`)
	}))
	defer server.Close()
	configured, err := New(Config{BaseURL: server.URL, DisableAPIKey: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configured.Close() })
	ctx, output := modelLogContext(t)
	_, selected, err := NewModelRouter().RouteChat(ctx, configured, ChatRequest{Messages: []Message{{Role: RoleUser, Content: "private-prompt"}}}, []ModelCandidate{{Model: "primary"}, {Model: "secondary"}}, 0)
	if err != nil || selected != 1 {
		t.Fatalf("fallback = %d, %v", selected, err)
	}
	records := modelLogRecords(t, output)
	if len(records) != 4 {
		t.Fatalf("attempt logs = %+v", records)
	}
	assertModelLogPair(t, ctx, records[:2], "primary", "chat_completions", "failed", false)
	assertModelLogPair(t, ctx, records[2:], "secondary", "chat_completions", "completed", false)
	if records[0]["model_call_id"] == records[2]["model_call_id"] || records[1]["level"] != "ERROR" || records[1]["http_status"] != float64(503) || records[3]["usage_reported"] != false || records[3]["prompt_tokens"] != nil {
		t.Fatalf("fallback/usage details = %+v", records)
	}
}

type failingModelLogStream struct{ err error }

func (stream failingModelLogStream) Recv() (*ChatChunk, error) { return nil, stream.err }
func (stream failingModelLogStream) Close() error              { return nil }

func TestModelStreamFailureCancellationAndEarlyClose(t *testing.T) {
	for _, test := range []struct {
		name, status, level string
		err                 error
		closeEarly          bool
	}{
		{"receive failure", "failed", "ERROR", errors.New("private-stream-error"), false},
		{"cancellation", "cancelled", "INFO", context.Canceled, false},
		{"early close", "closed", "INFO", nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, output := modelLogContext(t)
			stream := &modelLogStream{Stream: failingModelLogStream{test.err}, call: startModelCall(ctx, "selected/model", "chat_completions", true)}
			if !test.closeEarly {
				if _, err := stream.Recv(); err != test.err {
					t.Fatalf("receive error changed: %v", err)
				}
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
			records := modelLogRecords(t, output)
			assertModelLogPair(t, ctx, records, "selected/model", "chat_completions", test.status, true)
			if records[1]["level"] != test.level {
				t.Fatalf("completion severity = %+v", records[1])
			}
		})
	}
}

func TestModelStreamOpenFailureIsLogged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(writer, `{"error":{"message":"private-provider-error","type":"invalid_request"}}`)
	}))
	defer server.Close()
	configured, err := New(Config{BaseURL: server.URL, DefaultModel: "selected/model", DisableAPIKey: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configured.Close() })
	ctx, output := modelLogContext(t)
	if _, err := configured.ChatStream(ctx, ChatRequest{Messages: []Message{{Role: RoleUser, Content: "private-prompt"}}}); err == nil {
		t.Fatal("failed stream unexpectedly opened")
	}
	assertModelLogPair(t, ctx, modelLogRecords(t, output), "selected/model", "chat_completions", "failed", true)
}

func TestEmbeddingModelCallLogsMetadataWithoutInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"model":"resolved/embedding","data":[{"embedding":[0.1],"index":0}],"usage":{"prompt_tokens":7,"total_tokens":7}}`)
	}))
	defer server.Close()
	configured, err := New(Config{BaseURL: server.URL, DefaultModel: "selected/embedding", DisableAPIKey: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configured.Close() })
	ctx, output := modelLogContext(t)
	if _, err := configured.Embed(ctx, EmbeddingRequest{Input: "private-embedding-input"}); err != nil {
		t.Fatal(err)
	}
	records := modelLogRecords(t, output)
	if len(records) != 2 || records[0]["model_call_id"] != records[1]["model_call_id"] || records[0]["requested_model"] != "selected/embedding" || records[0]["api_mode"] != "embeddings" || records[1]["response_model"] != "resolved/embedding" || records[1]["prompt_tokens"] != float64(7) || records[1]["usage_role"] != "embedding" || records[1]["request_id"] != diagnostics.RequestID(ctx) {
		t.Fatalf("embedding metadata = %+v", records)
	}
}
