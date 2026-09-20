package agentloop_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
	qtools "github.com/snowmerak/q/tools"
)

var (
	_ agentloop.ToolRuntime = (*qtools.Runtime)(nil)
	_ agentloop.ToolRuntime = (*qtools.RoleRuntime)(nil)
)

type scriptedModel struct {
	responses []*client.ChatResponse
	requests  []client.ChatRequest
}

func (m *scriptedModel) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	request.Messages = append([]client.Message(nil), request.Messages...)
	request.Tools = append([]client.Tool(nil), request.Tools...)
	m.requests = append(m.requests, request)
	if len(m.responses) == 0 {
		return nil, errors.New("unexpected model call")
	}
	response := m.responses[0]
	m.responses = m.responses[1:]
	return response, nil
}

type echoRuntime struct {
	calls []client.ToolCall
	value string
}

func (r *echoRuntime) Tools() []client.Tool {
	return []client.Tool{{
		Type: client.ToolTypeFunction,
		Function: client.FunctionDefinition{
			Name: "echo",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"text": map[string]any{"type": "string"}},
			},
		},
	}}
}

func (r *echoRuntime) Call(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
	r.calls = append(r.calls, call)
	return client.ToolResult{Content: r.value}, nil
}

func toolResponse(name, arguments string) *client.ChatResponse {
	return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
		Role: client.RoleAssistant,
		ToolCalls: []client.ToolCall{{
			Type:     client.ToolTypeFunction,
			Function: client.FunctionCall{Name: name, Arguments: arguments},
		}},
	}}}}
}

func textResponse(content string) *client.ChatResponse {
	return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
		Role: client.RoleAssistant, Content: content,
	}}}}
}

func TestRunSupportsExternalModelAndToolRuntime(t *testing.T) {
	model := &scriptedModel{responses: []*client.ChatResponse{
		toolResponse(agentloop.TaskStartToolName, `{"objective":"echo a value","completion_criteria":["value echoed"]}`),
		toolResponse("echo", `{"text":"hello"}`),
		toolResponse(agentloop.TaskCompleteToolName, `{"outcome":"succeeded","summary":"Echoed the value.","verification":["echo returned hello"]}`),
		textResponse("acknowledged"),
	}}
	runtime := &echoRuntime{value: "hello"}
	var kinds []agentloop.EventKind

	result, err := agentloop.Run(t.Context(), agentloop.Request{
		Client: model,
		Tools:  runtime,
		Messages: []client.Message{{
			Role: client.RoleUser, Content: "echo hello",
		}},
		Model:     "test-model",
		MaxRounds: 8,
	}, agentloop.Hooks{Event: func(event agentloop.Event) error {
		kinds = append(kinds, event.Kind)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "succeeded" || result.ToolCalls != 1 {
		t.Fatalf("result = %#v", result)
	}
	if result.Response == nil || len(result.Response.Choices) == 0 {
		t.Fatal("missing final response")
	}
	final := result.Response.Choices[0].Message
	if final.Name != agentloop.TaskCompletionReplyName || final.Content != "Echoed the value.\n\nVerification:\n- echo returned hello" {
		t.Fatalf("final message = %#v", final)
	}
	if len(runtime.calls) != 1 || runtime.calls[0].Function.Name != "echo" {
		t.Fatalf("runtime calls = %#v", runtime.calls)
	}
	if len(model.requests) != 4 {
		t.Fatalf("model requests = %d", len(model.requests))
	}
	for index, request := range model.requests[:3] {
		if len(request.Tools) != len(runtime.Tools())+len(agentloop.OrchestrationTools()) {
			t.Fatalf("request %d tools = %d", index, len(request.Tools))
		}
	}
	if !containsEvent(kinds, agentloop.EventTaskStarted) || !containsEvent(kinds, agentloop.EventTaskCompleted) {
		t.Fatalf("event kinds = %#v", kinds)
	}
}

type compactingModel struct {
	normalCalls  int
	compactCalls int
}

func (m *compactingModel) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	if len(request.Tools) == 0 && len(request.Messages) > 0 &&
		strings.Contains(request.Messages[0].Content, "session continuation checkpoint") {
		m.compactCalls++
		body, _ := json.Marshal(memory.Checkpoint{ActiveWork: []string{"condensed tool evidence"}})
		return textResponse(string(body)), nil
	}
	m.normalCalls++
	if m.normalCalls == 1 {
		return toolResponse("echo", `{}`), nil
	}
	return textResponse("done"), nil
}

func TestRunCompactsToolHistoryInsidePublicLoop(t *testing.T) {
	model := &compactingModel{}
	hugeResult := strings.Repeat("large result ", 5_000)
	runtime := &echoRuntime{value: hugeResult}
	compactions := 0

	result, err := agentloop.Run(t.Context(), agentloop.Request{
		Client: model,
		Tools:  runtime,
		Messages: []client.Message{
			{Role: client.RoleSystem, Content: "keep this contract"},
			{Role: client.RoleUser, Content: "read the large value"},
		},
		Model: "test-model",
		ContextPolicy: memory.Policy{
			ContextWindow: 16_000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07,
		},
		MaxRounds: 4,
	}, agentloop.Hooks{Event: func(event agentloop.Event) error {
		if event.Kind == agentloop.EventCompaction {
			compactions++
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if model.compactCalls != 1 || compactions != 1 {
		t.Fatalf("compaction calls/events = %d/%d", model.compactCalls, compactions)
	}
	var contextText strings.Builder
	for _, message := range result.Context {
		contextText.WriteString(message.TextContent())
	}
	if !strings.Contains(contextText.String(), "condensed tool evidence") {
		t.Fatalf("compacted context omitted checkpoint: %#v", result.Context)
	}
	if strings.Contains(contextText.String(), hugeResult) {
		t.Fatal("compacted context retained oversized tool output")
	}
}

func containsEvent(events []agentloop.EventKind, want agentloop.EventKind) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}
