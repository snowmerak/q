package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func TestChatMapsSystemToolsResultsAndCacheUsage(t *testing.T) {
	step := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("x-api-key") != "secret" || request.Header.Get("anthropic-version") != defaultAnthropicVersion {
			t.Fatalf("headers = %#v", request.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["max_tokens"] != float64(65536) {
			t.Fatalf("default max_tokens = %#v, want 65536", body["max_tokens"])
		}
		if body["output_config"].(map[string]any)["effort"] != "medium" {
			t.Fatalf("reasoning effort = %#v", body["output_config"])
		}
		switch step {
		case 0:
			system := body["system"].([]any)
			if system[0].(map[string]any)["cache_control"].(map[string]any)["type"] != "ephemeral" {
				t.Fatalf("system = %#v", system)
			}
			tools := body["tools"].([]any)
			if tools[0].(map[string]any)["name"] != "lookup" ||
				body["tool_choice"].(map[string]any)["type"] != "tool" {
				t.Fatalf("tool request = %#v", body)
			}
			_, _ = io.WriteString(writer, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"key":"demo"}}],"stop_reason":"tool_use","usage":{"input_tokens":100,"output_tokens":10,"cache_creation_input_tokens":1200,"cache_read_input_tokens":0}}`)
		case 1:
			messages := body["messages"].([]any)
			last := messages[len(messages)-1].(map[string]any)
			blocks := last["content"].([]any)
			if last["role"] != "user" || blocks[0].(map[string]any)["type"] != "tool_result" ||
				blocks[0].(map[string]any)["tool_use_id"] != "toolu_1" {
				t.Fatalf("tool result = %#v", last)
			}
			_, _ = io.WriteString(writer, `{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-5","content":[{"type":"text","text":"VALUE_OK"}],"stop_reason":"end_turn","usage":{"input_tokens":50,"output_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":1200}}`)
		default:
			t.Fatal("unexpected request")
		}
		step++
	}))
	defer server.Close()

	provider := New(WithBaseURL(server.URL), WithAPIKey("secret"))
	request := llmprovider.ChatRequest{
		Model: "claude-sonnet-5",
		Messages: []llmprovider.Message{
			{Role: llmprovider.RoleSystem, ContentParts: []llmprovider.MessageContentPart{{
				"type": "text", "text": "stable", "cache_control": map[string]any{"type": "ephemeral"},
			}}},
			{Role: llmprovider.RoleUser, Content: "lookup"},
		},
		Tools: []llmprovider.Tool{{Type: llmprovider.ToolTypeFunction, Function: llmprovider.FunctionDefinition{
			Name: "lookup", Parameters: map[string]any{"type": "object"},
		}}},
		ToolChoice:      llmprovider.NamedToolChoice("lookup"),
		ReasoningEffort: "medium",
	}
	first, err := provider.Chat(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Choices[0].FinishReason != "tool_calls" || first.Choices[0].Message.ToolCalls[0].ID != "toolu_1" {
		t.Fatalf("first = %#v", first)
	}
	request.Messages = append(request.Messages, first.Choices[0].Message, llmprovider.Message{
		Role: llmprovider.RoleTool, ToolCallID: "toolu_1", Content: `{"value":"VALUE_OK"}`,
	})
	second, err := provider.Chat(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Choices[0].Message.Content != "VALUE_OK" || second.Usage.PromptDetails.CachedTokens != 1200 ||
		second.Usage.PromptTokens != 1250 {
		t.Fatalf("second = %#v", second)
	}
}

func TestChatStreamMapsTextAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"model\":\"claude-sonnet-5\",\"usage\":{\"input_tokens\":10,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":100}}}\n\n")
		_, _ = io.WriteString(writer, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
		_, _ = io.WriteString(writer, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n")
		_, _ = io.WriteString(writer, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	stream, err := New(WithBaseURL(server.URL), WithAPIKey("secret")).ChatStream(context.Background(), llmprovider.ChatRequest{
		Model: "claude-sonnet-5", Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	textChunk, err := stream.Recv()
	if err != nil || textChunk.Choices[0].Delta.Content != "hello" {
		t.Fatalf("text chunk = %#v, err = %v", textChunk, err)
	}
	final, err := stream.Recv()
	if err != nil || final.Choices[0].FinishReason != "stop" || final.Usage.PromptDetails.CachedTokens != 100 {
		t.Fatalf("final chunk = %#v, err = %v", final, err)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("final error = %v", err)
	}
}

func TestSonnetFiveRejectsNonDefaultSampling(t *testing.T) {
	temperature := 0.2
	_, err := New().messagePayload(llmprovider.ChatRequest{
		Model: "claude-sonnet-5", Temperature: &temperature,
		Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "hi"}},
	}, false)
	if err == nil || !strings.Contains(err.Error(), "temperature") {
		t.Fatalf("error = %v", err)
	}
}

func TestListModelsPreservesContextLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/models" || request.URL.Query().Get("limit") != "1000" {
			t.Fatalf("request URL = %s", request.URL)
		}
		_, _ = io.WriteString(writer, `{"data":[{`+
			`"id":"claude-test","type":"model","created_at":"2026-06-29T00:00:00Z",`+
			`"max_input_tokens":1000000,"max_tokens":128000,`+
			`"capabilities":{"effort":{"supported":true,"low":{"supported":true},"medium":{"supported":true},"high":{"supported":true},"xhigh":{"supported":false},"max":{"supported":true}}}}]}`)
	}))
	defer server.Close()

	models, err := New(WithBaseURL(server.URL), WithAPIKey("secret")).ListModels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reasoning := models[0].Capabilities.Reasoning
	if len(models) != 1 || models[0].ID != "claude-test" || models[0].Object != "model" ||
		models[0].OwnedBy != "anthropic" || models[0].Created != 1782691200 ||
		models[0].ContextLength != 1000000 || models[0].MaxOutputTokens != 128000 ||
		reasoning.Control != llmprovider.ReasoningControlEffort || reasoning.DefaultEffort != "high" ||
		!slices.Equal(reasoning.SupportedEfforts, []string{"low", "medium", "high", "max"}) {
		t.Fatalf("models = %#v", models)
	}
}
