package chatgpt

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func TestPromptCacheKeySetsSessionHeaderOnWire(t *testing.T) {
	for _, mode := range []string{"responses", "responses-stream", "chat", "chat-stream"} {
		for _, test := range []struct {
			name, key, session, want string
		}{
			{"automatic", "cache_saved-session", "", "cache_saved-session"},
			{"another-session", "cache_other-session", "", "cache_other-session"},
			{"explicit-header", "cache_saved-session", "custom-session", "custom-session"},
			{"no-key", "", "", ""},
		} {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				calls := 0
				transport := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
					calls++
					if got := r.Header.Get("session-id"); got != test.want {
						t.Errorf("session-id = %q, want %q", got, test.want)
					}
					if r.Header.Get("originator") != "llm-provider" {
						t.Error("application identity changed")
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						return nil, err
					}
					if key, _ := body["prompt_cache_key"].(string); key != test.key {
						t.Errorf("prompt_cache_key = %q, want %q", key, test.key)
					}
					data := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-test\",\"model\":\"test\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n"
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(data)), Request: r}, nil
				})}
				provider := newTestProvider(t, t.TempDir(), transport)
				seedAccount(t, provider)
				if err := provider.withState(t.Context(), func(saved *state) error {
					account := saved.Accounts[saved.Active]
					account.Access, account.Expires = "test-access", 1<<62
					saved.Accounts[saved.Active] = account
					return provider.save(saved)
				}); err != nil {
					t.Fatal(err)
				}
				headers := http.Header{"X-Request-Id": {"request-test"}}
				if test.session != "" {
					headers.Set("session-id", test.session)
				}
				original := headers.Clone()
				body := map[string]any{"model": "test", "input": "hello"}
				extra := make(map[string]any)
				if test.key != "" {
					body["prompt_cache_key"], extra["prompt_cache_key"] = test.key, test.key
				}
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				request := llmprovider.ChatRequest{Model: "test", Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "hello"}}, Extra: extra, Headers: headers}
				switch mode {
				case "responses":
					_, err = provider.CreateResponse(t.Context(), raw, headers)
				case "responses-stream":
					var stream llmprovider.ResponseStream
					stream, err = provider.CreateResponseStream(t.Context(), raw, headers)
					if err == nil {
						_, err = stream.Recv()
						_ = stream.Close()
					}
				case "chat":
					_, err = provider.Chat(t.Context(), request)
				case "chat-stream":
					var stream llmprovider.Stream
					stream, err = provider.ChatStream(t.Context(), request)
					if err == nil {
						_, err = stream.Recv()
						_ = stream.Close()
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if calls != 1 || !reflect.DeepEqual(headers, original) {
					t.Fatalf("calls=%d, caller headers changed=%v", calls, !reflect.DeepEqual(headers, original))
				}
			})
		}
	}
}
