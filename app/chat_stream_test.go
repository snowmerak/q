package app

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

type scriptedChatStream struct {
	chunks []*client.ChatChunk
	index  int
}

func (s *scriptedChatStream) Recv() (*client.ChatChunk, error) {
	if s.index >= len(s.chunks) {
		return nil, io.EOF
	}
	chunk := s.chunks[s.index]
	s.index++
	return chunk, nil
}

func (s *scriptedChatStream) Close() error { return nil }

type scriptedStreamingClient struct {
	request client.ChatRequest
	stream  client.Stream
}

func (c *scriptedStreamingClient) Chat(context.Context, client.ChatRequest) (*client.ChatResponse, error) {
	return nil, nil
}

func (c *scriptedStreamingClient) ChatStream(_ context.Context, request client.ChatRequest) (client.Stream, error) {
	c.request = request
	return c.stream, nil
}

func (c *scriptedStreamingClient) ListModels(context.Context) ([]client.Model, error) {
	return nil, nil
}
func (c *scriptedStreamingClient) Close() error { return nil }

func TestConsumeChatStreamSeparatesThinkingAndResponse(t *testing.T) {
	usage := client.Usage{PromptTokens: 12, CompletionTokens: 7, TotalTokens: 19}
	configured := &scriptedStreamingClient{stream: &scriptedChatStream{chunks: []*client.ChatChunk{
		{ID: "turn-1", Model: "local", Raw: json.RawMessage(`{"choices":[{"delta":{"reasoning":"Inspecting "}}]}`), Choices: []client.Choice{{
			Delta: &client.Message{Role: client.RoleAssistant},
		}}},
		{ID: "turn-1", Model: "local", Raw: json.RawMessage(`{"choices":[{"delta":{"reasoning":"the code."}}]}`), Choices: []client.Choice{{
			Delta: &client.Message{Role: client.RoleAssistant},
		}}},
		{ID: "turn-1", Model: "local", Choices: []client.Choice{{
			Delta: &client.Message{Role: client.RoleAssistant, Content: "Done "},
		}}},
		{ID: "turn-1", Model: "local", Usage: &usage, Choices: []client.Choice{{
			Delta: &client.Message{Role: client.RoleAssistant, Content: "now."}, FinishReason: "stop",
		}}},
	}}}
	var deltas []chatStreamDelta
	response, received, err := consumeChatStream(t.Context(), configured, client.ChatRequest{Model: "local"}, func(delta chatStreamDelta) bool {
		deltas = append(deltas, delta)
		return true
	})
	if err != nil || !received {
		t.Fatalf("received = %v, err = %v", received, err)
	}
	if response.Choices[0].Message.Content != "Done now." || response.Usage.TotalTokens != 19 {
		t.Fatalf("response = %#v", response)
	}
	if len(deltas) != 4 || deltas[0].Kind != chatStreamThinking || !deltas[0].Start || deltas[1].Start ||
		deltas[2].Kind != chatStreamResponse || !deltas[2].Start || deltas[3].Start {
		t.Fatalf("deltas = %#v", deltas)
	}
	options, ok := configured.request.Extra["stream_options"].(map[string]any)
	if !ok || options["include_usage"] != true {
		t.Fatalf("stream options = %#v", configured.request.Extra)
	}
}

func TestConsumeChatStreamReadsCompatibleReasoningAndMergesToolFragments(t *testing.T) {
	raw := json.RawMessage(`{"choices":[{"delta":{"reasoning_content":"checking"}}]}`)
	configured := &scriptedStreamingClient{stream: &scriptedChatStream{chunks: []*client.ChatChunk{
		{Raw: raw, Choices: []client.Choice{{Delta: &client.Message{Role: client.RoleAssistant}}}},
		{Choices: []client.Choice{{Delta: &client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
			Index: 0, ID: "call-1", Type: client.ToolTypeFunction,
			Function: client.FunctionCall{Name: "lookup", Arguments: `{"city":`},
		}}}}}},
		{Choices: []client.Choice{{Delta: &client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
			Index: 0, Function: client.FunctionCall{Arguments: `"Seoul"}`},
		}}}, FinishReason: "tool_calls"}}},
	}}}
	var thinking string
	response, _, err := consumeChatStream(t.Context(), configured, client.ChatRequest{}, func(delta chatStreamDelta) bool {
		if delta.Kind == chatStreamThinking {
			thinking += delta.Content
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := response.Choices[0].Message.ToolCalls
	if thinking != "checking" || len(calls) != 1 || calls[0].ID != "call-1" || calls[0].Function.Name != "lookup" ||
		calls[0].Function.Arguments != `{"city":"Seoul"}` || response.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("thinking = %q, response = %#v", thinking, response)
	}
}

func TestConsumeChatStreamSeparatesCodexGatewayPhases(t *testing.T) {
	configured := &scriptedStreamingClient{stream: &scriptedChatStream{chunks: []*client.ChatChunk{
		{
			Choices: []client.Choice{{
				Phase: "commentary", Delta: &client.Message{Role: client.RoleAssistant, Content: "working"},
			}},
		},
		{
			Choices: []client.Choice{{
				Phase: "final_answer", Delta: &client.Message{Role: client.RoleAssistant, Content: "done"},
			}},
		},
	}}}
	var thinking string
	response, _, err := consumeChatStream(t.Context(), configured, client.ChatRequest{}, func(delta chatStreamDelta) bool {
		if delta.Kind == chatStreamThinking {
			thinking += delta.Content
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if thinking != "working" || response.Choices[0].Message.Content != "done" {
		t.Fatalf("thinking = %q, response = %#v", thinking, response)
	}
}

func TestNormalizeDisplaySymbolsSkipsCode(t *testing.T) {
	source := "A $\\rightarrow$ B and x \\leq y. `\\rightarrow`\n```text\n$\\rightarrow$\n```"
	want := "A → B and x ≤ y. `\\rightarrow`\n```text\n$\\rightarrow$\n```"
	if got := normalizeDisplaySymbols(source); got != want {
		t.Fatalf("normalized = %q, want %q", got, want)
	}
}

func TestModelSupportsChatStreaming(t *testing.T) {
	gatewayConfig := gateway.Config{Providers: []gateway.ProviderConfig{
		{ID: "local", Type: "openai-compatible", Enabled: true},
		{ID: "router", Type: "openrouter", Enabled: true},
		{ID: "xai", Type: "xai", Enabled: true},
		{ID: "codex", Type: "codex", Enabled: true},
		{ID: "claude", Type: "anthropic", Enabled: true},
	}}
	groups := map[string]config.ModelGroupConfig{
		"local-only": {Candidates: []config.ModelCandidateConfig{{Model: "local/slow-a"}, {Model: "local/slow-b"}}},
		"mixed":      {Candidates: []config.ModelCandidateConfig{{Model: "local/slow"}, {Model: "codex/fast"}}},
	}
	tests := []struct {
		model string
		want  bool
	}{
		{model: "local/slow", want: true},
		{model: "router/model", want: true},
		{model: "xai/model", want: true},
		{model: "codex/fast", want: true},
		{model: "claude/model", want: false},
		{model: "group/local-only", want: true},
		{model: "group/mixed", want: true},
		{model: "missing/model", want: false},
	}
	for _, test := range tests {
		if got := modelSupportsChatStreaming(gatewayConfig, groups, test.model, nil); got != test.want {
			t.Errorf("model %q streaming = %v, want %v", test.model, got, test.want)
		}
	}
}
