package client_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
)

func TestResponsesModeThroughGatewayReplaysToolsAndCacheAffinity(t *testing.T) {
	var upstreamBodies []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(writer, `{"data":[{"id":"model"}]}`)
		case "/v1/responses":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode upstream body: %v", err)
			}
			upstreamBodies = append(upstreamBodies, body)
			if len(upstreamBodies) == 1 {
				_, _ = io.WriteString(writer, `{"id":"resp_1","model":"model","status":"completed","output":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}]}`)
			} else {
				_, _ = io.WriteString(writer, `{"id":"resp_2","model":"model","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"found"}]}]}`)
			}
		default:
			t.Errorf("unexpected upstream path: %s", request.URL.Path)
			http.NotFound(writer, request)
		}
	}))
	defer upstream.Close()
	configuredGateway, err := gateway.New(gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "native", Type: "openai-compatible", Enabled: true, BaseURL: upstream.URL + "/v1", Models: []string{"model"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configuredGateway.Close() })
	server := httptest.NewServer(configuredGateway.Handler())
	defer server.Close()
	configured, err := client.New(client.Config{
		BaseURL: server.URL + "/v1", APIKey: "test", ForwardUsageMetadata: true,
		ModelAPIModes: map[string]string{"native/model": "responses"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configured.Close() })
	user := client.Message{Role: client.RoleUser, Content: "look up"}
	first, err := configured.Chat(t.Context(), client.ChatRequest{Model: "native/model", Messages: []client.Message{user}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := configured.Chat(t.Context(), client.ChatRequest{
		Model: "native/model", ConversationID: first.ConversationID,
		Messages: []client.Message{user, first.Choices[0].Message, {Role: client.RoleTool, ToolCallID: "call_1", Content: "result"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Choices[0].Message.Content != "found" || len(upstreamBodies) != 2 {
		t.Fatalf("second response = %#v; upstream calls = %d", second, len(upstreamBodies))
	}
	for _, body := range upstreamBodies {
		if _, found := body["cache_affinity_id"]; found {
			t.Fatalf("Gateway-only field leaked upstream: %#v", body)
		}
		if body["model"] != "model" || body["prompt_cache_key"] != first.ConversationID {
			t.Fatalf("upstream routing and affinity = %#v", body)
		}
	}
	input := upstreamBodies[1]["input"].([]any)
	if len(input) != 4 || input[1].(map[string]any)["encrypted_content"] != "opaque" || input[3].(map[string]any)["call_id"] != "call_1" {
		t.Fatalf("upstream replay = %#v", input)
	}
}
