package openai

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

// TestIntegrationExternalGatewayCodexDelegatedTool deliberately talks only to
// an already-running OpenAI-compatible gateway. It verifies that this provider
// can drive a Codex App Server tool round trip without importing gateway code.
func TestIntegrationExternalGatewayCodexDelegatedTool(t *testing.T) {
	baseURL := os.Getenv("OPENAI_PROVIDER_GATEWAY_INTEGRATION_BASE_URL")
	if baseURL == "" {
		t.Skip("set OPENAI_PROVIDER_GATEWAY_INTEGRATION_BASE_URL to an active gateway /v1 URL")
	}
	model := os.Getenv("OPENAI_PROVIDER_GATEWAY_INTEGRATION_MODEL")
	if model == "" {
		model = "codex/gpt-5.6-luna"
	}
	client := llmprovider.New(New(WithBaseURL(baseURL)))
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	request := llmprovider.ChatRequest{
		Model: model,
		Messages: []llmprovider.Message{{
			Role:    llmprovider.RoleUser,
			Content: "You must call lookup_external_gateway with key demo. Do not answer before calling it.",
		}},
		Tools: []llmprovider.Tool{{
			Type: llmprovider.ToolTypeFunction,
			Function: llmprovider.FunctionDefinition{
				Name: "lookup_external_gateway", Description: "Returns a test value from the external caller.",
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
		t.Fatalf("gateway returned no tool call: %#v", first)
	}
	call := first.Choices[0].Message.ToolCalls[0]
	if call.Function.Name != "lookup_external_gateway" ||
		first.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("unexpected tool call: %#v", first)
	}

	request.ConversationID = first.ConversationID
	request.ToolChoice = llmprovider.ToolChoiceNone
	request.Messages = append(request.Messages, first.Choices[0].Message, llmprovider.Message{
		Role: llmprovider.RoleTool, ToolCallID: call.ID,
		Content: `{"value":"EXTERNAL_GATEWAY_TOOL_OK_359"}`,
	})
	second, err := client.Chat(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Choices) == 0 ||
		!strings.Contains(second.Choices[0].Message.Content, "EXTERNAL_GATEWAY_TOOL_OK_359") {
		t.Fatalf("gateway did not use the tool result: %#v", second)
	}
}
