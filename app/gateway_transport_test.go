package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

type reconnectingTestRuntime struct {
	providerRuntime
	mu            sync.RWMutex
	endpoint, key string
}

func (runtime *reconnectingTestRuntime) Connection() (string, string) {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.endpoint, runtime.key
}

func (runtime *reconnectingTestRuntime) setConnection(endpoint, key string) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.endpoint, runtime.key = endpoint, key
}

func TestManagedGatewayRetryFollowsReplacement(t *testing.T) {
	for _, operation := range []string{"embedding", "chat", "chat stream", "responses chat", "responses chat stream", "responses stream", "models"} {
		t.Run(operation, func(t *testing.T) {
			runtime := &reconnectingTestRuntime{providerRuntime: &fakeProviderRuntime{}}
			var firstCalls, replacementCalls atomic.Int32
			replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				replacementCalls.Add(1)
				if r.Header.Get("Authorization") != "Bearer replacement-key" {
					t.Error("request used an old Gateway credential")
					w.WriteHeader(401)
					return
				}
				if r.URL.Path != "/v1/models" && !client.ValidUsageEventID(r.Header.Get(client.UsageEventIDHeader)) {
					t.Error("missing usage identity")
				}
				switch operation {
				case "embedding":
					_, _ = io.WriteString(w, `{"model":"test","data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
				case "chat":
					_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
				case "chat stream":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
				case "responses chat":
					_, _ = io.WriteString(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
				case "responses stream":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
				case "responses chat stream":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
				case "models":
					_, _ = io.WriteString(w, `{"data":[{"id":"test"}]}`)
				}
			}))
			defer replacement.Close()
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				firstCalls.Add(1)
				runtime.setConnection(replacement.URL+"/v1", "replacement-key")
				// Simulate shutdown while the old generation's response is read.
				w.Header().Set("Content-Length", "10000")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "{")
				w.(http.Flusher).Flush()
			}))
			defer first.Close()
			runtime.setConnection(first.URL+"/v1", "first-key")
			value := config.Default()
			value.Provider.Model = "test"
			value.Provider.RequestRetry = config.RequestRetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond}
			if operation == "responses chat" || operation == "responses chat stream" {
				value.ModelAPIModes = map[string]string{"test": "responses"}
			}
			configured, err := managedClientFactory(runtime, nil)(value)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = configured.Close() }()
			model := configured.(*client.Client)
			request := client.ChatRequest{Model: "test", Messages: []client.Message{{Role: client.RoleUser, Content: "hello"}}}
			switch operation {
			case "embedding":
				response, err := model.Embed(t.Context(), client.EmbeddingRequest{Model: "test", Input: []string{"hello"}})
				if err != nil || response == nil || len(response.Data) != 1 {
					t.Fatalf("embedding = %+v, %v", response, err)
				}
			case "chat", "responses chat":
				response, err := model.Chat(t.Context(), request)
				if err != nil || response == nil || response.Choices[0].Message.Content != "ok" {
					t.Fatalf("chat = %+v, %v", response, err)
				}
			case "chat stream", "responses chat stream":
				stream, err := model.ChatStream(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = stream.Close() }()
				chunk, err := stream.Recv()
				if err != nil || chunk == nil || chunk.Choices[0].Delta.Content != "ok" {
					t.Fatalf("stream = %+v, %v", chunk, err)
				}
			case "responses stream":
				stream, err := model.CreateResponseStream(t.Context(), json.RawMessage(`{"model":"test","input":"hello"}`), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = stream.Close() }()
				event, err := stream.Recv()
				if err != nil || event == nil || event.Event != "response.completed" {
					t.Fatalf("response stream = %+v, %v", event, err)
				}
			case "models":
				models, err := model.ListModels(t.Context())
				if err != nil || len(models) != 1 {
					t.Fatalf("models = %+v, %v", models, err)
				}
			}
			if firstCalls.Load() != 1 || replacementCalls.Load() != 1 {
				t.Fatalf("requests: old=%d new=%d", firstCalls.Load(), replacementCalls.Load())
			}
		})
	}
}
