package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func TestGatewayDirectChatStreamAndEmbed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"m1"},{"id":"emb1"}]}`)
		case "/v1/chat/completions":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			var req struct {
				Model          string `json:"model"`
				Stream         bool   `json:"stream"`
				PromptCacheKey string `json:"prompt_cache_key"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatal(err)
			}
			if req.Model != "m1" {
				t.Fatalf("backend model = %q, want m1", req.Model)
			}
			if req.PromptCacheKey == "" {
				t.Fatal("expected prompt_cache_key prepared by gateway")
			}
			if req.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello \"}}]}\n\n")
				_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"world\"},\"finish_reason\":\"stop\"}]}\n\n")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"r1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi there"},"finish_reason":"stop"}]}`)
		case "/v1/embeddings":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"emb1","usage":{"prompt_tokens":2,"total_tokens":2}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	gw, err := New(Config{Providers: []ProviderConfig{{
		ID:      "mock",
		Type:    "openai-compatible",
		Enabled: true,
		BaseURL: upstream.URL + "/v1",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()

	// Sync Chat
	resp, err := gw.Chat(t.Context(), llmprovider.ChatRequest{
		Model:    "mock/m1",
		Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("gw.Chat: %v", err)
	}
	if resp.Model != "mock/m1" {
		t.Fatalf("resp.Model = %q, want mock/m1", resp.Model)
	}
	if resp.ConversationID == "" {
		t.Fatal("expected generated ConversationID on response")
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.TextContent() != "hi there" {
		t.Fatalf("unexpected response choices: %#v", resp.Choices)
	}

	// Stream Chat
	stream, err := gw.ChatStream(t.Context(), llmprovider.ChatRequest{
		Model:    "mock/m1",
		Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("gw.ChatStream: %v", err)
	}
	defer stream.Close()

	var text strings.Builder
	var convID string
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("stream.Recv: %v", err)
		}
		if chunk.Model != "mock/m1" {
			t.Fatalf("chunk.Model = %q, want mock/m1", chunk.Model)
		}
		if convID == "" {
			convID = chunk.ConversationID
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta != nil {
			text.WriteString(chunk.Choices[0].Delta.TextContent())
		}
	}
	if text.String() != "hello world" {
		t.Fatalf("streamed text = %q, want hello world", text.String())
	}
	if convID == "" {
		t.Fatal("expected ConversationID on streamed chunks")
	}

	// Embed
	embResp, err := gw.Embed(t.Context(), llmprovider.EmbeddingRequest{
		Model: "mock/emb1",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("gw.Embed: %v", err)
	}
	if embResp.Model != "mock/emb1" {
		t.Fatalf("embResp.Model = %q, want mock/emb1", embResp.Model)
	}

	// Unknown prefix error
	if _, err := gw.Chat(t.Context(), llmprovider.ChatRequest{Model: "unknown/m1"}); err == nil {
		t.Fatal("expected error for unknown prefix")
	}

	// Google AI Studio host auto-resolves to generic kind (no prompt_cache_key injection).
	if kind := effectiveProviderKind(ProviderConfig{
		Type:    "openai-compatible",
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
	}); kind != "generic" {
		t.Fatalf("effectiveProviderKind for Google AI Studio = %q, want generic", kind)
	}
}
