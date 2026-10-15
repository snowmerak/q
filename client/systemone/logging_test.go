package systemone

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/snowmerak/q/diagnostics"
)

func TestDecisionLogsModelAndAvailableUsageWithoutNativePayload(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.Header().Set("X-Private", "private-response-header")
				writer.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = io.WriteString(writer, `{"model":"resolved/decision","answers":{"x":{"type":"choice","choice":"private-answer"}},"usage":{"input_tokens":5,"output_tokens":null}}`)
				} else {
					_, _ = io.WriteString(writer, `{"error":"private-error"}`)
				}
			}))
			defer server.Close()
			configured, err := New(Config{BaseURL: server.URL, APIKey: "private-api-key"})
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			ctx := diagnostics.WithRequestID(diagnostics.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&output, nil))), "")
			_, err = configured.EvaluateRaw(ctx, json.RawMessage(`{"model":"selected/decision","state":"private-state","questions":{"x":{"type":"choice","instructions":"private-instructions","criteria":{"a":"private-criteria"}}}}`), CallOptions{IdempotencyKey: "private-idempotency-key"})
			if (err == nil) != (status == http.StatusOK) {
				t.Fatalf("decision error = %v", err)
			}
			if strings.Contains(output.String(), "private-") {
				t.Fatalf("native payload leaked: %s", output.String())
			}
			lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
			if len(lines) != 2 {
				t.Fatalf("decision lifecycle = %s", output.String())
			}
			var first, last map[string]any
			if err := json.Unmarshal(lines[0], &first); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(lines[1], &last); err != nil {
				t.Fatal(err)
			}
			if first["msg"] != "model.call.started" || last["msg"] != "model.call.finished" || first["model_call_id"] != last["model_call_id"] || last["request_id"] != diagnostics.RequestID(ctx) || last["requested_model"] != "selected/decision" || last["api_mode"] != "system_one" || last["duration_ms"] == nil {
				t.Fatalf("decision correlation = %+v, %+v", first, last)
			}
			if status == http.StatusOK {
				if last["level"] != "INFO" || last["status"] != "completed" || last["response_model"] != "resolved/decision" || last["prompt_tokens"] != float64(5) || last["completion_tokens"] != nil {
					t.Fatalf("decision usage = %+v", last)
				}
			} else if last["level"] != "ERROR" || last["status"] != "failed" || last["http_status"] != float64(status) {
				t.Fatalf("decision failure = %+v", last)
			}
		})
	}
}
