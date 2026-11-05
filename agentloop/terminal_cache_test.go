package agentloop_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/workspace"
)

// Check the wire after Gateway's provider-specific cache mapping, rather than
// only the request handed to the client. This does not measure live cache hits.
func TestTerminalRequestPreservesUpstreamPromptAndToolSettings(t *testing.T) {
	for _, kind := range []string{"grok", "openrouter"} {
		for _, mode := range []string{"chat_completions", "responses"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				var mu sync.Mutex
				var bodies []map[string]any
				var affinities []string
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/v1/models" {
						_, _ = io.WriteString(w, `{"data":[{"id":"model"}]}`)
						return
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					mu.Lock()
					bodies = append(bodies, body)
					affinities = append(affinities, r.Header.Get("X-Grok-Conv-Id"))
					round := len(bodies)
					mu.Unlock()
					if round > 3 {
						http.Error(w, "unexpected extra request", 400)
						return
					}
					name, args := "lookup", `{}`
					if round == 2 {
						name, args = "task_complete", `{"outcome":"succeeded","summary":"done"}`
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if mode == "responses" {
						item := map[string]any{"type": "function_call", "call_id": fmt.Sprintf("call-%d", round), "name": name, "arguments": args}
						if round == 3 {
							item = map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "acknowledged"}}}
						}
						data, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("response-%d", round), "model": "model", "status": "completed", "output": []any{item}}})
						_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", data)
					} else {
						delta := map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call-%d", round), "type": "function", "function": map[string]any{"name": name, "arguments": args}}}}
						reason := "tool_calls"
						if round == 3 {
							delta, reason = map[string]any{"role": "assistant", "content": "acknowledged"}, "stop"
						}
						data, _ := json.Marshal(map[string]any{"model": "model", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}})
						_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
					}
				}))
				t.Cleanup(upstream.Close)
				gw, err := gateway.New(gateway.Config{Providers: []gateway.ProviderConfig{{ID: "probe", Type: "openai-compatible", Kind: kind, Enabled: true, BaseURL: upstream.URL + "/v1", Models: []string{"model"}}}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = gw.Close() })
				server := httptest.NewServer(gw.Handler())
				t.Cleanup(server.Close)
				configured, err := client.New(client.Config{BaseURL: server.URL + "/v1", DisableAPIKey: true, ForwardUsageMetadata: true, ModelAPIModes: map[string]string{"probe/model": mode}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = configured.Close() })
				events := make(chan agentloop.Event)
				go agentloop.RunAgentLoop(t.Context(), agentloop.Request{Client: configured, Tools: &scriptedTools{name: "lookup", content: "found"}, Model: "probe/model", Stream: true, ActiveTask: &workspace.ActiveTask{Objective: "inspect"}, Messages: []client.Message{{Role: client.RoleSystem, Content: "stable instructions"}, {Role: client.RoleUser, Content: "inspect and finish"}}}, events)
				for event := range events {
					if err := event.Err(); err != nil {
						t.Fatal(err)
					}
				}
				mu.Lock()
				defer mu.Unlock()
				if len(bodies) != 3 {
					t.Fatalf("requests=%d", len(bodies))
				}
				before, after := bodies[1], bodies[2]
				messageField := "messages"
				if mode == "responses" {
					messageField = "input"
				}
				prefix, continued := before[messageField].([]any), after[messageField].([]any)
				if len(continued) <= len(prefix) || !reflect.DeepEqual(prefix, continued[:len(prefix)]) {
					t.Fatal("terminal request rewrote the existing conversation prefix")
				}
				for key, value := range before {
					if key != messageField && !reflect.DeepEqual(value, after[key]) {
						t.Errorf("terminal request changed %s: %#v -> %#v", key, value, after[key])
					}
				}
				for key := range after {
					if _, exists := before[key]; key != messageField && !exists {
						t.Errorf("terminal request introduced %s", key)
					}
				}
				if kind == "grok" && mode == "chat_completions" && (affinities[1] == "" || affinities[1] != affinities[2]) {
					t.Fatal("Grok cache affinity changed")
				}
			})
		}
	}
}
