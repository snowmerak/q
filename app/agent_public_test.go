package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/client"
	qtools "github.com/snowmerak/q/tools"
)

type embeddedClient struct {
	requests []client.ChatRequest
}

func (c *embeddedClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	request.Messages = append([]client.Message(nil), request.Messages...)
	request.Tools = append([]client.Tool(nil), request.Tools...)
	c.requests = append(c.requests, request)
	if len(c.requests) == 1 {
		return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
			Role: client.RoleAssistant,
			ToolCalls: []client.ToolCall{{
				ID: "echo-1", Type: client.ToolTypeFunction,
				Function: client.FunctionCall{Name: "echo", Arguments: `{"text":"hello"}`},
			}},
		}}}}, nil
	}
	return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
		Role: client.RoleAssistant, Content: "embedded loop complete",
	}}}}, nil
}

func (*embeddedClient) ListModels(context.Context) ([]client.Model, error) { return nil, nil }
func (*embeddedClient) Close() error                                       { return nil }

type embeddedTools struct {
	calls []client.ToolCall
}

func (*embeddedTools) Tools() []client.Tool {
	return []client.Tool{{
		Type: client.ToolTypeFunction,
		Function: client.FunctionDefinition{
			Name: "echo", Description: "echo text",
			Parameters: map[string]any{"type": "object"},
		},
	}}
}

func (*embeddedTools) Environment() qtools.HostEnvironment {
	return qtools.HostEnvironment{OS: "test-os", Architecture: "test-arch", Shell: "test-shell"}
}

func (r *embeddedTools) Call(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
	r.calls = append(r.calls, call)
	return client.ToolResult{Content: `{"text":"hello"}`}, nil
}

var _ app.ChatClient = (*embeddedClient)(nil)
var _ app.AgentToolRuntime = (*embeddedTools)(nil)

func TestPublicAgentLoopRunsToolRoundFromExternalPackage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Keep embedded changes focused."), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := &embeddedTools{}
	base := []client.Message{{Role: client.RoleSystem, Content: "Host-owned system prompt."}}
	messages := app.PrepareWorkspaceMessages(base, app.WorkspaceMessageOptions{
		Root: root, Tools: tools,
	})
	if len(base) != 1 {
		t.Fatalf("input messages were mutated: %#v", base)
	}
	joined := joinPublicMessageContent(messages)
	for _, expected := range []string{"Keep embedded changes focused.", root, "task_start"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("prepared messages do not contain %q:\n%s", expected, joined)
		}
	}
	messages = append(messages, client.Message{Role: client.RoleUser, Content: "Echo hello."})

	configuredClient := &embeddedClient{}
	events := make(chan app.AgentEvent)
	go app.RunAgentLoop(t.Context(), app.AgentLoopRequest{
		Client: configuredClient, Tools: tools, Model: "test-model",
		Messages: messages, WorkingDirectory: root,
	}, events)

	var toolCallSeen, toolMessageSeen bool
	var final app.AgentLoopResult
	var resultSeen bool
	for event := range events {
		if err := event.Err(); err != nil {
			t.Fatal(err)
		}
		if call, ok := event.ToolCall(); ok && call.Function.Name == "echo" {
			toolCallSeen = true
		}
		if message, ok := event.Message(); ok && message.Role == client.RoleTool && message.Name == "echo" {
			toolMessageSeen = true
		}
		if result, ok := event.Result(); ok {
			final, resultSeen = result, true
		}
	}

	if !toolCallSeen || !toolMessageSeen || len(tools.calls) != 1 {
		t.Fatalf("tool call event=%v tool message=%v runtime calls=%d", toolCallSeen, toolMessageSeen, len(tools.calls))
	}
	if !resultSeen || final.Response == nil || len(final.Response.Choices) == 0 ||
		final.Response.Choices[0].Message.Content != "embedded loop complete" || final.ToolCalls != 1 {
		t.Fatalf("final result = %#v", final)
	}
	if len(configuredClient.requests) != 2 {
		t.Fatalf("model requests = %d", len(configuredClient.requests))
	}
}

func joinPublicMessageContent(messages []client.Message) string {
	var contents []string
	for _, message := range messages {
		contents = append(contents, message.Content)
	}
	return strings.Join(contents, "\n")
}
