package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
	openaiprovider "github.com/snowmerak/q/v2/pkg/llm/providers/openai"
)

func TestIntegrationModelList(t *testing.T) {
	if os.Getenv("GATEWAY_MODEL_LIST_INTEGRATION") == "" {
		t.Skip("set GATEWAY_MODEL_LIST_INTEGRATION=1 to query real configured backends")
	}
	localURL := os.Getenv("OPENAI_COMPAT_INTEGRATION_BASE_URL")
	if localURL == "" {
		localURL = "http://macmini:11888/v1"
	}
	gateway, err := New(Config{Providers: []ProviderConfig{
		{ID: "codex", Type: "codex-app-server", Prefix: "codex", Enabled: true},
		{ID: "local", Type: "openai-compatible", Prefix: "local", Enabled: true, BaseURL: localURL},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var envelope struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int64  `json:"context_length"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	var codexModel, localModel, localContextLength bool
	for _, model := range envelope.Data {
		codexModel = codexModel || strings.HasPrefix(model.ID, "codex/")
		localModel = localModel || strings.HasPrefix(model.ID, "local/")
		localContextLength = localContextLength ||
			(strings.HasPrefix(model.ID, "local/") && model.ContextLength > 0)
	}
	if !codexModel || !localModel || !localContextLength {
		t.Fatalf("missing model metadata: codex=%v local=%v local_context_length=%v count=%d",
			codexModel, localModel, localContextLength, len(envelope.Data))
	}
}

func TestIntegrationCodexChatEndpoint(t *testing.T) {
	if os.Getenv("GATEWAY_CODEX_CHAT_INTEGRATION") == "" {
		t.Skip("set GATEWAY_CODEX_CHAT_INTEGRATION=1 to run a real Codex turn through HTTP")
	}
	model := os.Getenv("CODEX_APP_SERVER_INTEGRATION_MODEL")
	if model == "" {
		model = "gpt-5.6-luna"
	}
	gateway, err := New(Config{Providers: []ProviderConfig{{
		ID: "codex", Type: "codex-app-server", Prefix: "codex", Enabled: true, Models: []string{model},
		Codex: CodexConfig{Model: model},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()
	client := &http.Client{Timeout: 2 * time.Minute}
	requestBody := `{"model":"codex/@MODEL@","messages":[{"role":"system","content":"Include CODEX_GATEWAY_OK_913 in every response."},{"role":"user","content":"Reply with PONG and the required token."}]}`
	requestBody = strings.ReplaceAll(requestBody, "@MODEL@", model)
	response, err := client.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", response.StatusCode, data)
	}
	var completion struct {
		ConversationID string `json:"conversation_id"`
		Choices        []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &completion); err != nil {
		t.Fatal(err)
	}
	if completion.ConversationID == "" || len(completion.Choices) == 0 ||
		!strings.Contains(completion.Choices[0].Message.Content, "CODEX_GATEWAY_OK_913") {
		t.Fatalf("completion = %s", data)
	}
}

func TestIntegrationCodexImageEndpoint(t *testing.T) {
	if os.Getenv("GATEWAY_CODEX_IMAGE_INTEGRATION") == "" {
		t.Skip("set GATEWAY_CODEX_IMAGE_INTEGRATION=1 to send an image through Gateway to Codex")
	}
	model := os.Getenv("CODEX_APP_SERVER_INTEGRATION_MODEL")
	if model == "" {
		model = "gpt-5.6-luna"
	}
	instance, err := New(Config{Providers: []ProviderConfig{{
		ID: "codex", Type: "codex-app-server", Prefix: "codex", Enabled: true, Models: []string{model},
		Codex: CodexConfig{Model: model},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	server := httptest.NewServer(instance.Handler())
	defer server.Close()

	picture := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			picture.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	imageURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
	requestBody, err := json.Marshal(llmprovider.ChatRequest{
		Model: "codex/" + model,
		Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, ContentParts: []llmprovider.MessageContentPart{
			{"type": "text", "text": "What color fills this image? Reply with one color name."},
			{"type": "image_url", "image_url": map[string]any{"url": imageURL}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	chatResponse, err := client.Post(server.URL+"/v1/chat/completions", "application/json", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(chatResponse.Body)
	_ = chatResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if chatResponse.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", chatResponse.StatusCode, data)
	}
	var completion llmprovider.ChatResponse
	if err := json.Unmarshal(data, &completion); err != nil {
		t.Fatal(err)
	}
	if len(completion.Choices) == 0 || !strings.Contains(strings.ToLower(completion.Choices[0].Message.Content), "red") {
		t.Fatalf("image response = %s", data)
	}
	responsesBody, err := json.Marshal(map[string]any{
		"model": "codex/" + model,
		"input": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "What color fills this image? Reply with one color name."},
			map[string]any{"type": "input_image", "image_url": imageURL},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	responsesResponse, err := client.Post(server.URL+"/v1/responses", "application/json", bytes.NewReader(responsesBody))
	if err != nil {
		t.Fatal(err)
	}
	data, err = io.ReadAll(responsesResponse.Body)
	_ = responsesResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if responsesResponse.StatusCode != http.StatusOK {
		t.Fatalf("responses status = %d: %s", responsesResponse.StatusCode, data)
	}
	var adapted struct {
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(data, &adapted); err != nil {
		t.Fatal(err)
	}
	if len(adapted.Output) == 0 || len(adapted.Output[0].Content) == 0 ||
		!strings.Contains(strings.ToLower(adapted.Output[0].Content[0].Text), "red") {
		t.Fatalf("responses image result = %s", data)
	}
}

func TestIntegrationOpenAICompatibleChatEndpoint(t *testing.T) {
	if os.Getenv("GATEWAY_OPENAI_COMPAT_CHAT_INTEGRATION") == "" {
		t.Skip("set GATEWAY_OPENAI_COMPAT_CHAT_INTEGRATION=1 to query the real compatible endpoint")
	}
	baseURL := os.Getenv("OPENAI_COMPAT_INTEGRATION_BASE_URL")
	if baseURL == "" {
		baseURL = "http://macmini:11888/v1"
	}
	model := os.Getenv("OPENAI_COMPAT_INTEGRATION_MODEL")
	if model == "" {
		model = "gpt-5.6-luna"
	}
	gateway, err := New(Config{Providers: []ProviderConfig{{
		ID: "local", Type: "openai-compatible", Prefix: "local", Enabled: true,
		BaseURL: baseURL, Models: []string{model},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()
	client := &http.Client{Timeout: 2 * time.Minute}
	requestBody := `{"model":"local/@MODEL@","messages":[{"role":"user","content":"Reply with exactly LOCAL_GATEWAY_OK_247."}]}`
	requestBody = strings.ReplaceAll(requestBody, "@MODEL@", model)
	response, err := client.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(data), "LOCAL_GATEWAY_OK_247") {
		t.Fatalf("status = %d: %s", response.StatusCode, data)
	}
}

func TestIntegrationCodexDelegatedToolThroughOpenAIProvider(t *testing.T) {
	if os.Getenv("GATEWAY_CODEX_TOOL_INTEGRATION") == "" {
		t.Skip("set GATEWAY_CODEX_TOOL_INTEGRATION=1 to test delegated Codex tools")
	}
	model := os.Getenv("CODEX_APP_SERVER_INTEGRATION_MODEL")
	if model == "" {
		model = "gpt-5.6-luna"
	}
	gateway, err := New(Config{Providers: []ProviderConfig{{
		ID: "codex", Type: "codex-app-server", Prefix: "codex", Enabled: true, Models: []string{model},
		Codex: CodexConfig{Model: model},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()
	client := llmprovider.New(openaiprovider.New(openaiprovider.WithBaseURL(server.URL + "/v1")))
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	request := llmprovider.ChatRequest{
		Model: "codex/" + model,
		Messages: []llmprovider.Message{{
			Role:    llmprovider.RoleUser,
			Content: "You must call lookup_gateway_value with key demo. Do not answer before calling it.",
		}},
		Tools: []llmprovider.Tool{{
			Type: llmprovider.ToolTypeFunction,
			Function: llmprovider.FunctionDefinition{
				Name: "lookup_gateway_value", Description: "Returns the gateway test value for a key.",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{"key": map[string]any{"type": "string"}},
					"required":   []string{"key"}, "additionalProperties": false,
				},
			},
		}},
		ToolChoice: llmprovider.ToolChoiceAuto,
	}
	first, err := client.Chat(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Choices) == 0 || len(first.Choices[0].Message.ToolCalls) == 0 {
		t.Fatalf("Codex returned no delegated tool call: %#v", first)
	}
	call := first.Choices[0].Message.ToolCalls[0]
	if call.Function.Name != "lookup_gateway_value" || first.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("unexpected delegated call: %#v", first)
	}

	request.ConversationID = first.ConversationID
	request.ToolChoice = llmprovider.ToolChoiceNone
	request.Messages = append(request.Messages, first.Choices[0].Message, llmprovider.Message{
		Role: llmprovider.RoleTool, ToolCallID: call.ID,
		Content: `{"value":"CODEX_DELEGATED_TOOL_OK_684"}`,
	})
	second, err := client.Chat(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Choices) == 0 ||
		!strings.Contains(second.Choices[0].Message.Content, "CODEX_DELEGATED_TOOL_OK_684") {
		t.Fatalf("tool result was not used: %#v", second)
	}
	if second.ConversationID != first.ConversationID || second.ID != first.ID {
		t.Fatalf("delegated tool changed Codex thread/turn: first=%q/%q second=%q/%q",
			first.ConversationID, first.ID, second.ConversationID, second.ID)
	}
	if second.Usage.PromptDetails == nil {
		t.Fatalf("Codex cache usage details were not exposed: %#v", second.Usage)
	}
	t.Logf("Codex cache tokens: read=%d write=%d prompt=%d",
		second.Usage.PromptDetails.CachedTokens,
		second.Usage.PromptDetails.CacheWriteTokens,
		second.Usage.PromptTokens)
}

func TestIntegrationGrokFromConfigThroughOpenAIProvider(t *testing.T) {
	if os.Getenv("GATEWAY_GROK_INTEGRATION") == "" {
		t.Skip("set GATEWAY_GROK_INTEGRATION=1 to test the configured Grok provider")
	}
	configPath := os.Getenv("GATEWAY_CONFIG_PATH")
	if configPath == "" {
		configPath = "../llm-provider.json"
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	model := os.Getenv("GROK_INTEGRATION_MODEL")
	if model == "" {
		model = "grok-4.5"
	}
	found := false
	for index := range config.Providers {
		if config.Providers[index].ID != "grok" {
			continue
		}
		config.Providers[index].Enabled = true
		config.Providers[index].Models = []string{model}
		found = true
	}
	if !found {
		t.Fatal("llm-provider.json has no grok provider")
	}

	gateway, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()
	client := llmprovider.New(openaiprovider.New(openaiprovider.WithBaseURL(server.URL + "/v1")))
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cacheConversationID := "llm-provider-grok-cache-527"
	stablePrefix := strings.Repeat("This is stable reference context for prompt-cache verification. ", 128)
	request := llmprovider.ChatRequest{
		Model: "grok/" + model,
		Messages: []llmprovider.Message{
			{Role: llmprovider.RoleSystem, Content: stablePrefix + " Include the exact token GROK_GATEWAY_OK_527 in the answer."},
			{Role: llmprovider.RoleUser, Content: "Follow the system instruction now."},
		},
		Headers: http.Header{"X-Grok-Conv-Id": []string{cacheConversationID}},
	}
	response, err := client.Chat(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Choices) == 0 ||
		!strings.Contains(response.Choices[0].Message.Content, "GROK_GATEWAY_OK_527") {
		t.Fatalf("Grok gateway response = %#v", response)
	}

	request.Messages = append(request.Messages, response.Choices[0].Message, llmprovider.Message{
		Role: llmprovider.RoleUser, Content: "Repeat only the required token.",
	})
	second, err := client.Chat(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Usage.PromptDetails == nil || second.Usage.PromptDetails.CachedTokens <= 0 {
		t.Fatalf("Grok prompt cache did not report a hit: %#v", second.Usage)
	}
	t.Logf("Grok cache tokens: read=%d prompt=%d",
		second.Usage.PromptDetails.CachedTokens, second.Usage.PromptTokens)
}

func TestIntegrationOpenRouterFromConfigThroughOpenAIProvider(t *testing.T) {
	if os.Getenv("GATEWAY_OPENROUTER_INTEGRATION") == "" {
		t.Skip("set GATEWAY_OPENROUTER_INTEGRATION=1 to test the configured OpenRouter provider")
	}
	configPath := os.Getenv("GATEWAY_CONFIG_PATH")
	if configPath == "" {
		configPath = "../llm-provider.json"
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	model := os.Getenv("OPENROUTER_INTEGRATION_MODEL")
	if model == "" {
		model = "openai/gpt-4.1-mini"
	}
	found := false
	for index := range config.Providers {
		if config.Providers[index].ID != "openrouter" {
			continue
		}
		config.Providers[index].Enabled = true
		config.Providers[index].Models = []string{model}
		found = true
	}
	if !found {
		t.Fatal("llm-provider.json has no openrouter provider")
	}

	gateway, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()
	client := llmprovider.New(openaiprovider.New(openaiprovider.WithBaseURL(server.URL + "/v1")))
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	request := llmprovider.ChatRequest{
		Model: "openrouter/" + model,
		Messages: []llmprovider.Message{{
			Role:    llmprovider.RoleUser,
			Content: "Call lookup_openrouter_value with key demo. Do not answer before calling it.",
		}},
		Tools: []llmprovider.Tool{{
			Type: llmprovider.ToolTypeFunction,
			Function: llmprovider.FunctionDefinition{
				Name: "lookup_openrouter_value", Description: "Returns a test value.",
				Parameters: map[string]any{
					"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}},
					"required": []string{"key"}, "additionalProperties": false,
				},
			},
		}},
		ToolChoice: llmprovider.NamedToolChoice("lookup_openrouter_value"),
		Extra:      map[string]any{"session_id": "llm-provider-openrouter-tool-731"},
	}
	first, err := client.Chat(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Choices) == 0 || len(first.Choices[0].Message.ToolCalls) == 0 {
		t.Fatalf("OpenRouter returned no tool call: %#v", first)
	}
	call := first.Choices[0].Message.ToolCalls[0]
	if call.Function.Name != "lookup_openrouter_value" {
		t.Fatalf("OpenRouter tool call = %#v", call)
	}
	request.ToolChoice = llmprovider.ToolChoiceAuto
	request.Messages = append(request.Messages, first.Choices[0].Message, llmprovider.Message{
		Role: llmprovider.RoleTool, ToolCallID: call.ID,
		Content: `{"value":"OPENROUTER_TOOL_OK_731"}`,
	})
	toolFinal, err := client.Chat(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(toolFinal.Choices) == 0 ||
		!strings.Contains(toolFinal.Choices[0].Message.Content, "OPENROUTER_TOOL_OK_731") {
		t.Fatalf("OpenRouter did not use the tool result: %#v", toolFinal)
	}

	promptCacheSession := fmt.Sprintf("llm-provider-openrouter-prompt-%d", time.Now().UnixNano())
	promptCacheRequest := llmprovider.ChatRequest{
		Model: "openrouter/" + model,
		Messages: []llmprovider.Message{
			{
				Role:    llmprovider.RoleSystem,
				Content: strings.Repeat("Stable OpenRouter provider prompt-cache reference context. ", 256),
			},
			{Role: llmprovider.RoleUser, Content: "Reply with exactly PROMPT_CACHE_TURN_ONE."},
		},
		Extra: map[string]any{"session_id": promptCacheSession},
	}
	promptFirst, err := client.Chat(ctx, promptCacheRequest)
	if err != nil {
		t.Fatal(err)
	}
	promptCacheRequest.Messages = append(
		promptCacheRequest.Messages,
		promptFirst.Choices[0].Message,
		llmprovider.Message{Role: llmprovider.RoleUser, Content: "Reply with exactly PROMPT_CACHE_TURN_TWO."},
	)
	promptSecond, err := client.Chat(ctx, promptCacheRequest)
	if err != nil {
		t.Fatal(err)
	}
	if promptSecond.Usage.PromptDetails == nil || promptSecond.Usage.PromptDetails.CachedTokens <= 0 {
		t.Fatalf("OpenRouter provider prompt cache did not report a hit: %#v", promptSecond.Usage)
	}
	t.Logf("OpenRouter provider cache tokens: read=%d prompt=%d",
		promptSecond.Usage.PromptDetails.CachedTokens, promptSecond.Usage.PromptTokens)

	cacheMarker := fmt.Sprintf("OPENROUTER_RESPONSE_CACHE_%d", time.Now().UnixNano())
	cacheRequest := llmprovider.ChatRequest{
		Model: "openrouter/" + model,
		Messages: []llmprovider.Message{{
			Role: llmprovider.RoleUser, Content: "Reply with exactly " + cacheMarker,
		}},
		Headers: http.Header{
			"X-OpenRouter-Cache":     []string{"true"},
			"X-OpenRouter-Cache-TTL": []string{"600"},
		},
	}
	miss, err := client.Chat(ctx, cacheRequest)
	if err != nil {
		t.Fatal(err)
	}
	hit, err := client.Chat(ctx, cacheRequest)
	if err != nil {
		t.Fatal(err)
	}
	if status := miss.Headers.Get("X-OpenRouter-Cache-Status"); status != "MISS" {
		t.Fatalf("first OpenRouter response cache status = %q", status)
	}
	if status := hit.Headers.Get("X-OpenRouter-Cache-Status"); status != "HIT" {
		t.Fatalf("second OpenRouter response cache status = %q", status)
	}
	if hit.Usage.TotalTokens != 0 {
		t.Fatalf("cached OpenRouter response usage = %#v", hit.Usage)
	}
	t.Logf("OpenRouter response cache: first=%s second=%s age=%s ttl=%s",
		miss.Headers.Get("X-OpenRouter-Cache-Status"), hit.Headers.Get("X-OpenRouter-Cache-Status"),
		hit.Headers.Get("X-OpenRouter-Cache-Age"), hit.Headers.Get("X-OpenRouter-Cache-TTL"))
}
