package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

func TestManagedClientUsesSavedProviderAPIMode(t *testing.T) {
	for _, test := range []struct{ name, kind, providerMode, modelMode, path string }{
		{"saved Chat on Grok", "grok", "chat_completions", "", "/v1/chat/completions"},
		{"saved Responses on generic", "generic", "responses", "", "/v1/responses"},
		{"explicit model Chat wins", "grok", "responses", "chat_completions", "/v1/chat/completions"},
		{"unset Grok stays Chat", "grok", "", "", "/v1/chat/completions"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := ""
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				if path == "/v1/responses" {
					_, _ = io.WriteString(w, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`)
				} else {
					_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
				}
			}))
			defer server.Close()
			runtime := &fakeProviderRuntime{endpoint: server.URL + "/v1", apiKey: "test", config: gateway.Config{Providers: []gateway.ProviderConfig{{ID: "provider", Prefix: "models", Type: "openai-compatible", Kind: test.kind, Enabled: true}}}}
			value := config.Default()
			value.Provider.Model = "models/backend"
			if test.providerMode != "" {
				value.ProviderAPIModes = map[string]string{"provider": test.providerMode}
			}
			if test.modelMode != "" {
				value.ModelAPIModes = map[string]string{"models/backend": test.modelMode}
			}
			configured, err := managedClientFactory(runtime, nil)(value)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := configured.Close(); err != nil {
					t.Error(err)
				}
			})
			result, err := configured.Chat(t.Context(), client.ChatRequest{Model: "models/backend", Messages: []client.Message{{Role: client.RoleUser, Content: "hello"}}})
			if err != nil || result == nil || result.Choices[0].Message.Content != "ok" {
				t.Fatalf("result=%v error=%v", result, err)
			}
			if path != test.path {
				t.Fatalf("request path=%s want %s", path, test.path)
			}
		})
	}
}
