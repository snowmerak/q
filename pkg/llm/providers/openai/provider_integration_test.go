package openai

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func integrationClient(t *testing.T) (*llmprovider.Client, string) {
	t.Helper()
	baseURL := os.Getenv("OPENAI_COMPAT_INTEGRATION_BASE_URL")
	if baseURL == "" {
		t.Skip("set OPENAI_COMPAT_INTEGRATION_BASE_URL to run compatibility integration tests")
	}
	model := os.Getenv("OPENAI_COMPAT_INTEGRATION_MODEL")
	if model == "" {
		model = "gpt-5.6-luna"
	}
	return llmprovider.New(New(WithBaseURL(baseURL))), model
}

func integrationContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 2*time.Minute)
}

func TestIntegrationSystemPrompt(t *testing.T) {
	client, model := integrationClient(t)
	defer client.Close()
	ctx, cancel := integrationContext(t)
	defer cancel()
	temperature, maxTokens := 0.0, 256
	response, err := client.Chat(ctx, llmprovider.ChatRequest{
		Model: model, Temperature: &temperature, MaxCompletionTokens: &maxTokens,
		Messages: []llmprovider.Message{
			{Role: llmprovider.RoleSystem, Content: "Your response must contain the exact token SYSTEM_PROMPT_OK_731. Do not omit or alter it."},
			{Role: llmprovider.RoleUser, Content: "Follow the system instruction now."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Choices[0].Message.Content, "SYSTEM_PROMPT_OK_731") {
		t.Fatalf("system prompt was not followed: %q", response.Choices[0].Message.Content)
	}
}

func TestIntegrationToolCallingRoundTrip(t *testing.T) {
	client, model := integrationClient(t)
	defer client.Close()
	ctx, cancel := integrationContext(t)
	defer cancel()
	maxTokens := 256
	request := llmprovider.ChatRequest{
		Model: model, MaxCompletionTokens: &maxTokens,
		Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "Use lookup_test_value with key demo, then report its result."}},
		Tools: []llmprovider.Tool{{Type: llmprovider.ToolTypeFunction, Function: llmprovider.FunctionDefinition{
			Name: "lookup_test_value", Description: "Looks up a test value by key.",
			Parameters: map[string]any{
				"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}},
				"required": []string{"key"}, "additionalProperties": false,
			},
		}}},
		ToolChoice: llmprovider.NamedToolChoice("lookup_test_value"),
	}
	response, err := client.Chat(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Choices[0].Message.ToolCalls) == 0 {
		t.Fatalf("model returned no tool call: %#v", response.Choices[0])
	}
	call := response.Choices[0].Message.ToolCalls[0]
	if call.Function.Name != "lookup_test_value" {
		t.Fatalf("tool name = %q", call.Function.Name)
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
		t.Fatalf("arguments %q: %v", call.Function.Arguments, err)
	}

	request.ToolChoice = llmprovider.ToolChoiceAuto
	request.Messages = append(request.Messages, response.Choices[0].Message, llmprovider.Message{
		Role: llmprovider.RoleTool, ToolCallID: call.ID, Content: `{"value":"TOOL_RESULT_OK_904"}`,
	})
	finalResponse, err := client.Chat(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(finalResponse.Choices[0].Message.Content, "TOOL_RESULT_OK_904") {
		t.Fatalf("tool result was not used: %q", finalResponse.Choices[0].Message.Content)
	}
}

func TestIntegrationModifiedContext(t *testing.T) {
	client, model := integrationClient(t)
	defer client.Close()
	ctx, cancel := integrationContext(t)
	defer cancel()
	maxTokens := 256
	response, err := client.Chat(ctx, llmprovider.ChatRequest{
		Model: model, MaxCompletionTokens: &maxTokens,
		Messages: []llmprovider.Message{
			{Role: llmprovider.RoleSystem, Content: "The most recent context correction is authoritative. Include its exact value in your answer."},
			{Role: llmprovider.RoleUser, Content: "The context value is CONTEXT_ALPHA_111."},
			{Role: llmprovider.RoleAssistant, Content: "I recorded CONTEXT_ALPHA_111."},
			{Role: llmprovider.RoleUser, Content: "Correction: replace the context value with CONTEXT_BETA_842."},
			{Role: llmprovider.RoleUser, Content: "What is the current context value?"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	content := response.Choices[0].Message.Content
	if !strings.Contains(content, "CONTEXT_BETA_842") {
		t.Fatalf("modified context was not used: %q", content)
	}
}
