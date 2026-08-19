package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewValidatesConfig(t *testing.T) {
	for _, config := range []Config{
		{BaseURL: "localhost:8080/v1"},
		{BaseURL: "ftp://localhost/v1"},
		{BaseURL: "https://localhost/v1?key=value"},
		{APIKey: "secret", DisableAPIKey: true},
	} {
		if _, err := New(config); err == nil {
			t.Errorf("New(%+v) unexpectedly succeeded", config)
		}
	}
}

func TestChatUsesProviderConfigurationAndDefaultModel(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("X-Client") != "q" || request.Header.Get("X-Request") != "turn" {
			t.Errorf("headers = %#v", request.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body["model"] != "default-model" || body["route"] != "request" || body["stream"] != false {
			t.Errorf("body = %#v", body)
		}
		if _, present := body["reasoning_effort"]; present {
			t.Errorf("empty reasoning_effort was sent: %#v", body)
		}
		if calls == 1 {
			if _, present := body["conversation_id"]; present {
				t.Errorf("first request had conversation_id: %#v", body)
			}
		} else if body["conversation_id"] != "cache_q_test" {
			t.Errorf("continued request conversation_id = %#v", body["conversation_id"])
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":"chat_1","model":"default-model","conversation_id":"cache_q_test","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	c, err := New(Config{
		BaseURL: server.URL + "/v1/", APIKey: "secret", DefaultModel: "default-model",
		Headers: http.Header{"X-Client": {"q"}}, BodyFields: map[string]any{"route": "default"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	response, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Extra:    map[string]any{"route": "request"}, Headers: http.Header{"X-Request": {"turn"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != "chat_1" || response.ConversationID != "cache_q_test" || response.Choices[0].Message.Content != "hello" {
		t.Fatalf("response = %#v", response)
	}
	if _, err := c.Chat(context.Background(), ChatRequest{
		ConversationID: response.ConversationID,
		Messages:       []Message{{Role: RoleUser, Content: "continue"}},
		Extra:          map[string]any{"route": "request"}, Headers: http.Header{"X-Request": {"turn"}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOptionalOpenAICompatibleEndpoints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(writer, `{"data":[{"id":"model-a","object":"model","capabilities":{"reasoning":{"supported":true,"control":"effort","supported_efforts":["low","high"],"default_effort":"high"}}}]}`)
		case "/v1/embeddings":
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["model"] != "model-a" {
				t.Errorf("embedding model = %#v", body["model"])
			}
			if body["dimensions"] != float64(2) {
				t.Errorf("embedding dimensions = %#v", body["dimensions"])
			}
			_, _ = io.WriteString(writer, `{"object":"list","model":"model-a","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
		case "/v1/responses":
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["model"] != "model-a" || body["stream"] != false {
				t.Errorf("responses body = %#v", body)
			}
			_, _ = io.WriteString(writer, `{"id":"resp_1","object":"response"}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	c, err := New(Config{BaseURL: server.URL + "/v1", DefaultModel: "model-a", DisableAPIKey: true})
	if err != nil {
		t.Fatal(err)
	}
	models, err := c.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "model-a" {
		t.Fatalf("models = %#v, err = %v", models, err)
	}
	if models[0].Capabilities == nil || models[0].Capabilities.Reasoning == nil {
		t.Fatalf("missing reasoning capabilities: %#v", models[0])
	}
	reasoning := models[0].Capabilities.Reasoning
	if reasoning.Control != ReasoningControlEffort || reasoning.DefaultEffort != "high" ||
		len(reasoning.SupportedEfforts) != 2 || reasoning.SupportedEfforts[0] != "low" {
		t.Fatalf("reasoning capabilities = %#v", reasoning)
	}
	dimensions := 2
	embedding, err := c.Embed(context.Background(), EmbeddingRequest{Input: "hello", Dimensions: &dimensions})
	if err != nil || len(embedding.Data) != 1 {
		t.Fatalf("embedding = %#v, err = %v", embedding, err)
	}
	response, err := c.CreateResponse(context.Background(), json.RawMessage(`{"input":"hello"}`), nil)
	if err != nil || !strings.Contains(string(response.Body), `"resp_1"`) {
		t.Fatalf("response = %#v, err = %v", response, err)
	}
}

func TestChatPreservesFunctionToolsAndModelOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		tools, _ := body["tools"].([]any)
		if body["model"] != "request-model" || body["reasoning_effort"] != "high" ||
			body["tool_choice"] != "required" || len(tools) != 1 {
			t.Errorf("body = %#v", body)
		}
		_, _ = io.WriteString(writer, `{"id":"chat_tool","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"key\":\"value\"}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	defer server.Close()
	c, err := New(Config{BaseURL: server.URL, DefaultModel: "default-model", DisableAPIKey: true})
	if err != nil {
		t.Fatal(err)
	}
	response, err := c.Chat(context.Background(), ChatRequest{
		Model: "request-model", Messages: []Message{{Role: RoleUser, Content: "look it up"}},
		ReasoningEffort: "high",
		Tools: []Tool{{Type: ToolTypeFunction, Function: FunctionDefinition{
			Name: "lookup", Parameters: map[string]any{"type": "object"},
		}}},
		ToolChoice: ToolChoiceRequired,
	})
	if err != nil {
		t.Fatal(err)
	}
	call := response.Choices[0].Message.ToolCalls[0]
	if call.ID != "call_1" || call.Function.Name != "lookup" || response.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("response = %#v", response)
	}
}

func TestStreamingChatAndResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		switch request.URL.Path {
		case "/chat/completions":
			_, _ = io.WriteString(writer, "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n")
		case "/responses":
			_, _ = io.WriteString(writer, "event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	c, err := New(Config{BaseURL: server.URL, DefaultModel: "model-a", DisableAPIKey: true})
	if err != nil {
		t.Fatal(err)
	}

	chat, err := c.ChatStream(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := chat.Recv()
	if err != nil || chunk.Choices[0].Delta.Content != "hello" {
		t.Fatalf("chunk = %#v, err = %v", chunk, err)
	}
	if _, err := chat.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("chat final error = %v", err)
	}

	responses, err := c.CreateResponseStream(context.Background(), json.RawMessage(`{"input":"hi"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	event, err := responses.Recv()
	if err != nil || event.Event != "response.output_text.delta" || !strings.Contains(string(event.Data), "hello") {
		t.Fatalf("event = %#v, err = %v", event, err)
	}
	if _, err := responses.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("responses final error = %v", err)
	}
}

func TestDisableAPIKeyOverridesEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "environment-secret")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if authorization := request.Header.Get("Authorization"); authorization != "" {
			t.Errorf("authorization = %q", authorization)
		}
		_, _ = io.WriteString(writer, `{"data":[]}`)
	}))
	defer server.Close()
	c, err := New(Config{BaseURL: server.URL, DisableAPIKey: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAPIErrorRemainsInspectable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(writer, `{"error":{"message":"bad request","type":"invalid_request_error","code":"bad"}}`)
	}))
	defer server.Close()
	c, err := New(Config{BaseURL: server.URL, DisableAPIKey: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusBadRequest || apiError.Code != "bad" {
		t.Fatalf("error = %T %v", err, err)
	}
}
