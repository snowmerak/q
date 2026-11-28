package codex

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func integrationProvider() *Provider {
	options := []Option{WithEphemeral(true), WithSandbox(SandboxReadOnly)}
	if model := os.Getenv("CODEX_APP_SERVER_INTEGRATION_MODEL"); model != "" {
		options = append(options, WithModel(model))
	}
	if instructions := os.Getenv("CODEX_APP_SERVER_BASE_INSTRUCTIONS"); instructions != "" {
		options = append(options, WithBaseInstructions(instructions))
	}
	return New(options...)
}

func TestIntegrationModelList(t *testing.T) {
	if os.Getenv("CODEX_APP_SERVER_INTEGRATION") == "" {
		t.Skip("set CODEX_APP_SERVER_INTEGRATION=1 to test an installed Codex App Server")
	}
	provider := New()
	defer provider.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	models, err := provider.ListModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) == 0 {
		t.Fatal("model/list returned no models")
	}
	for _, model := range models {
		if model.Capabilities != nil && model.Capabilities.Reasoning != nil &&
			len(model.Capabilities.Reasoning.SupportedEfforts) > 0 &&
			model.Capabilities.Reasoning.DefaultEffort != "" {
			return
		}
	}
	t.Fatalf("model/list returned no reasoning effort metadata: %#v", models)
}

func TestIntegrationDynamicTool(t *testing.T) {
	if os.Getenv("CODEX_APP_SERVER_TOOL_INTEGRATION") == "" {
		t.Skip("set CODEX_APP_SERVER_TOOL_INTEGRATION=1 to run a real Codex dynamic tool turn")
	}
	provider := integrationProvider()
	defer provider.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var called atomic.Bool
	response, err := provider.Chat(ctx, llmprovider.ChatRequest{
		Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "Call get_magic_number, then reply with the returned number."}},
		Tools: []llmprovider.Tool{{Type: llmprovider.ToolTypeFunction, Function: llmprovider.FunctionDefinition{
			Name: "get_magic_number", Description: "Returns the magic number. Always call this function when asked for the magic number.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		}}},
		ToolHandler: func(ctx context.Context, call llmprovider.ToolCall) (llmprovider.ToolResult, error) {
			called.Store(true)
			return llmprovider.ToolResult{Content: `{"number":42}`}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called.Load() {
		t.Fatalf("dynamic tool handler was not called; response: %q", response.Choices[0].Message.Content)
	}
	if !strings.Contains(response.Choices[0].Message.Content, "42") {
		t.Fatalf("unexpected response: %q", response.Choices[0].Message.Content)
	}
}

func TestIntegrationChat(t *testing.T) {
	if os.Getenv("CODEX_APP_SERVER_CHAT_INTEGRATION") == "" {
		t.Skip("set CODEX_APP_SERVER_CHAT_INTEGRATION=1 to run a real Codex turn")
	}
	provider := integrationProvider()
	defer provider.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	response, err := provider.Chat(ctx, llmprovider.ChatRequest{Messages: []llmprovider.Message{
		{Role: llmprovider.RoleUser, Content: "Reply with exactly PONG. Do not use tools."},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToUpper(response.Choices[0].Message.Content), "PONG") {
		t.Fatalf("unexpected response: %q", response.Choices[0].Message.Content)
	}
	if response.ConversationID == "" {
		t.Fatal("response has no conversation ID")
	}
}

func TestIntegrationEphemeralThreadWithToolsContinuity(t *testing.T) {
	if os.Getenv("CODEX_APP_SERVER_CONTINUITY_INTEGRATION") == "" {
		t.Skip("set CODEX_APP_SERVER_CONTINUITY_INTEGRATION=1 to test real ephemeral thread continuity")
	}
	provider := integrationProvider()
	defer provider.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	var calls atomic.Int64
	request := llmprovider.ChatRequest{
		ReasoningEffort: "low",
		Tools: []llmprovider.Tool{{Type: llmprovider.ToolTypeFunction, Function: llmprovider.FunctionDefinition{
			Name: "get_marker", Description: "Return the marker requested by the user.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		}}},
		ToolHandler: func(context.Context, llmprovider.ToolCall) (llmprovider.ToolResult, error) {
			calls.Add(1)
			return llmprovider.ToolResult{Content: `{"marker":"STABLE_TULIP47"}`}, nil
		},
	}
	var threadID string
	for index, prompt := range []string{
		"Call get_marker, remember the returned marker, and reply with only the marker.",
		"Without calling tools, reply with the marker from the previous turn.",
		"Again, without calling tools, reply with that same marker.",
	} {
		request.Messages = append(request.Messages, llmprovider.Message{Role: llmprovider.RoleUser, Content: prompt})
		response, err := provider.Chat(ctx, request)
		if err != nil {
			t.Fatalf("turn %d: %v", index+1, err)
		}
		if threadID == "" {
			threadID = response.ConversationID
		}
		if threadID == "" || response.ConversationID != threadID {
			t.Fatalf("turn %d changed thread from %q to %q", index+1, threadID, response.ConversationID)
		}
		if len(response.Choices) != 1 || !strings.Contains(response.Choices[0].Message.Content, "STABLE_TULIP47") {
			t.Fatalf("turn %d did not retain the marker: %#v", index+1, response)
		}
		cached := 0
		if response.Usage.PromptDetails != nil {
			cached = response.Usage.PromptDetails.CachedTokens
		}
		t.Logf("turn=%d thread=%s input=%d cached=%d output=%d", index+1, threadID,
			response.Usage.PromptTokens, cached, response.Usage.CompletionTokens)
		request.ConversationID = threadID
		request.Messages = append(request.Messages, response.Choices[0].Message)
	}
	if calls.Load() != 1 {
		t.Fatalf("dynamic tool calls = %d, want exactly one", calls.Load())
	}
}

func TestIntegrationImage(t *testing.T) {
	if os.Getenv("CODEX_APP_SERVER_IMAGE_INTEGRATION") == "" {
		t.Skip("set CODEX_APP_SERVER_IMAGE_INTEGRATION=1 to run a real Codex image turn")
	}
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
	provider := integrationProvider()
	t.Cleanup(func() { _ = provider.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	response, err := provider.Chat(ctx, llmprovider.ChatRequest{Messages: []llmprovider.Message{{
		Role: llmprovider.RoleUser,
		ContentParts: []llmprovider.MessageContentPart{
			{"type": "text", "text": "What color fills this image? Reply with one color name."},
			{"type": "image_url", "image_url": map[string]any{"url": imageURL}},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(response.Choices[0].Message.Content), "red") {
		t.Fatalf("image response = %q", response.Choices[0].Message.Content)
	}
}

// TestIntegrationPromptBaseline measures the prompt overhead supplied by Codex
// itself separately from the caller's chat messages. It is opt-in because it
// performs several real model calls.
func TestIntegrationPromptBaseline(t *testing.T) {
	if os.Getenv("CODEX_APP_SERVER_PROMPT_BASELINE_INTEGRATION") == "" {
		t.Skip("set CODEX_APP_SERVER_PROMPT_BASELINE_INTEGRATION=1 to measure Codex prompt overhead")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	options := []Option{WithEphemeral(true), WithSandbox(SandboxReadOnly)}
	if model := os.Getenv("CODEX_APP_SERVER_INTEGRATION_MODEL"); model != "" {
		options = append(options, WithModel(model))
	}
	provider := New(options...)
	defer provider.Close()

	measure := func(name string, request llmprovider.ChatRequest) *llmprovider.ChatResponse {
		t.Helper()
		response, err := provider.Chat(ctx, request)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		logPromptUsage(t, name, response.Usage)
		return response
	}

	first := measure("default_base", llmprovider.ChatRequest{Messages: []llmprovider.Message{{
		Role: llmprovider.RoleUser, Content: "Reply with exactly PONG. Do not use tools.",
	}}})
	measure("same_thread_second_turn", llmprovider.ChatRequest{
		ConversationID: first.ConversationID,
		Messages: []llmprovider.Message{{
			Role: llmprovider.RoleUser, Content: "Reply with exactly PONG again. Do not use tools.",
		}},
	})
	measure("later_new_thread_default_base", llmprovider.ChatRequest{Messages: []llmprovider.Message{{
		Role: llmprovider.RoleUser, Content: "Reply with exactly PONG. Do not use tools.",
	}}})

	systemProvider := New(options...)
	defer systemProvider.Close()
	systemResponse, err := systemProvider.Chat(ctx, llmprovider.ChatRequest{Messages: []llmprovider.Message{
		{Role: llmprovider.RoleSystem, Content: "Answer concisely."},
		{Role: llmprovider.RoleUser, Content: "Reply with exactly PONG. Do not use tools."},
	}})
	if err != nil {
		t.Fatalf("fresh_default_base_with_system_message: %v", err)
	}
	logPromptUsage(t, "fresh_default_base_with_system_message", systemResponse.Usage)

	replacementOptions := append([]Option(nil), options...)
	replacementOptions = append(replacementOptions, WithBaseInstructions(
		"You are a concise assistant. Reply exactly as requested.",
	))
	replacementProvider := New(replacementOptions...)
	defer replacementProvider.Close()
	replacementResponse, err := replacementProvider.Chat(ctx, llmprovider.ChatRequest{Messages: []llmprovider.Message{{
		Role: llmprovider.RoleUser, Content: "Reply with exactly PONG. Do not use tools.",
	}}})
	if err != nil {
		t.Fatalf("replacement_base: %v", err)
	}
	logPromptUsage(t, "replacement_base", replacementResponse.Usage)

	minimalOptions := []Option{
		WithEphemeral(true),
		WithSandbox(SandboxReadOnly),
		WithMinimal(),
		WithThreadStartParams(map[string]any{"config": map[string]any{
			"mcp_servers.openaiDeveloperDocs.enabled": false,
		}}),
	}
	if model := os.Getenv("CODEX_APP_SERVER_INTEGRATION_MODEL"); model != "" {
		minimalOptions = append(minimalOptions, WithModel(model))
	}
	minimalProvider := New(minimalOptions...)
	defer minimalProvider.Close()
	minimalResponse, err := minimalProvider.Chat(ctx, llmprovider.ChatRequest{Messages: []llmprovider.Message{{
		Role: llmprovider.RoleUser, Content: "Reply with exactly PONG. Do not use tools.",
	}}})
	if err != nil {
		t.Fatalf("minimal_thread_start: %v", err)
	}
	logPromptUsage(t, "minimal_thread_start", minimalResponse.Usage)
	minimalContinuation, err := minimalProvider.Chat(ctx, llmprovider.ChatRequest{
		ConversationID: minimalResponse.ConversationID,
		Messages: []llmprovider.Message{{
			Role: llmprovider.RoleUser, Content: "Reply with exactly PONG again. Do not use tools.",
		}},
	})
	if err != nil {
		t.Fatalf("minimal_same_thread_second_turn: %v", err)
	}
	if minimalContinuation.ConversationID != minimalResponse.ConversationID {
		t.Fatalf("minimal continuation changed thread: first=%q second=%q",
			minimalResponse.ConversationID, minimalContinuation.ConversationID)
	}
	logPromptUsage(t, "minimal_same_thread_second_turn", minimalContinuation.Usage)
}

func logPromptUsage(t *testing.T, name string, usage llmprovider.Usage) {
	t.Helper()
	if usage.PromptTokens <= 0 {
		t.Fatalf("%s reported no prompt tokens: %#v", name, usage)
	}
	if usage.PromptDetails == nil {
		t.Fatalf("%s reported no prompt token details: %#v", name, usage)
	}
	t.Logf("%s prompt=%d cached=%d cache_write=%d output=%d total=%d",
		name, usage.PromptTokens, usage.PromptDetails.CachedTokens,
		usage.PromptDetails.CacheWriteTokens, usage.CompletionTokens, usage.TotalTokens)
}

func TestIntegrationSystemPromptAndContext(t *testing.T) {
	if os.Getenv("CODEX_APP_SERVER_CONTEXT_INTEGRATION") == "" {
		t.Skip("set CODEX_APP_SERVER_CONTEXT_INTEGRATION=1 to test prompt and thread context")
	}
	provider := integrationProvider()
	defer provider.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	first, err := provider.Chat(ctx, llmprovider.ChatRequest{Messages: []llmprovider.Message{
		{Role: llmprovider.RoleSystem, Content: "Every answer must include CODEX_SYSTEM_OK_517."},
		{Role: llmprovider.RoleUser, Content: "Store CODEX_CONTEXT_ALPHA_103 as the current mutable working-context value, then state it with the system token."},
	}})
	if err != nil {
		t.Fatal(err)
	}
	firstContent := first.Choices[0].Message.Content
	if !strings.Contains(firstContent, "CODEX_SYSTEM_OK_517") || !strings.Contains(firstContent, "CODEX_CONTEXT_ALPHA_103") {
		t.Fatalf("system prompt was not applied: %q", firstContent)
	}

	second, err := provider.Chat(ctx, llmprovider.ChatRequest{
		ConversationID: first.ConversationID,
		Messages:       []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "Update the mutable working-context value from CODEX_CONTEXT_ALPHA_103 to CODEX_CONTEXT_BETA_842. Confirm the new value."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.Choices[0].Message.Content, "CODEX_CONTEXT_BETA_842") {
		t.Fatalf("context update was not acknowledged: %q", second.Choices[0].Message.Content)
	}

	third, err := provider.Chat(ctx, llmprovider.ChatRequest{
		ConversationID: first.ConversationID,
		Messages:       []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "What is the current context value? Include the system token too."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	thirdContent := third.Choices[0].Message.Content
	if !strings.Contains(thirdContent, "CODEX_CONTEXT_BETA_842") || !strings.Contains(thirdContent, "CODEX_SYSTEM_OK_517") {
		t.Fatalf("modified thread context was not retained: %q", thirdContent)
	}
}
