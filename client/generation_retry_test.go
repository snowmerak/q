package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGenerationTransportRetriesRespectReplayPolicy(t *testing.T) {
	for _, api := range []string{"chat", "chat-stream", "responses", "responses-stream", "raw-responses", "raw-responses-stream"} {
		for _, safe := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/safe=%v", api, safe), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if calls.Add(1) == 1 {
						w.WriteHeader(http.StatusBadGateway)
						_, _ = io.WriteString(w, `{"error":{"message":"response lost after remote execution"}}`)
						return
					}
					if r.URL.Path == "/responses" {
						body := `{"id":"response","model":"agent/test","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`
						var request struct{ Stream bool }
						_ = json.NewDecoder(r.Body).Decode(&request)
						if request.Stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", body)
						} else {
							_, _ = io.WriteString(w, body)
						}
						return
					}
					if api == "chat-stream" {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					} else {
						_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
					}
				}))
				defer server.Close()
				config := Config{BaseURL: server.URL, DisableAPIKey: true, DefaultModel: "agent/test", Retry: RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Millisecond}, GenerationConfig: func(_ context.Context, model string) (GenerationSettings, error) {
					if model != "agent/test" {
						t.Errorf("wrong replay policy model: %q", model)
					}
					return GenerationSettings{SafeReplay: safe}, nil
				}}
				if api == "responses" || api == "responses-stream" {
					config.ModelAPIModes = map[string]string{"agent/test": "responses"}
				}
				configured, err := New(config)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = configured.Close() }()
				request := ChatRequest{Messages: []Message{{Role: RoleUser, Content: "reply once"}}}
				switch api {
				case "chat", "responses":
					_, err = configured.Chat(t.Context(), request)
				case "chat-stream", "responses-stream":
					var stream Stream
					stream, err = configured.ChatStream(t.Context(), request)
					if err == nil {
						_, err = stream.Recv()
						_ = stream.Close()
					}
				case "raw-responses":
					_, err = configured.CreateResponse(t.Context(), json.RawMessage(`{"input":"reply once"}`), nil)
				case "raw-responses-stream":
					var stream ResponseStream
					stream, err = configured.CreateResponseStream(t.Context(), json.RawMessage(`{"input":"reply once","stream":true}`), nil)
					if err == nil {
						_, err = stream.Recv()
						_ = stream.Close()
					}
				}
				want := int32(1)
				if safe {
					want = 2
				}
				if calls.Load() != want || (err == nil) != safe {
					t.Fatalf("calls=%d want=%d err=%v", calls.Load(), want, err)
				}
			})
		}
	}
}

func TestUnsafeStreamDoesNotReconnectBeforeFirstChunk(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
	}))
	defer server.Close()
	configured, err := New(Config{BaseURL: server.URL, DisableAPIKey: true, Retry: RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = configured.Close() }()
	// Explicit caller policy takes precedence over the client's default.
	ctx := WithGenerationSettings(t.Context(), GenerationSettings{SafeReplay: false})
	stream, err := configured.ChatStream(ctx, ChatRequest{Model: "agent", Messages: []Message{{Role: RoleUser, Content: "reply once"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if _, err := stream.Recv(); err == nil || calls.Load() != 1 {
		t.Fatalf("unsafe stream reconnected: calls=%d err=%v", calls.Load(), err)
	}
}
