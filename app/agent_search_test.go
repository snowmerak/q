package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
)

func TestParseAgentSearchCommand(t *testing.T) {
	tests := []struct {
		command string
		query   string
		handled bool
	}{
		{command: "/agent:search", handled: true},
		{command: "/agent:search ACP session lifecycle", query: "ACP session lifecycle", handled: true},
		{command: "/agent:search-other", handled: false},
		{command: "research ACP", handled: false},
	}
	for _, test := range tests {
		query, handled := parseAgentSearchCommand(test.command)
		if query != test.query || handled != test.handled {
			t.Fatalf("parseAgentSearchCommand(%q) = %q, %v", test.command, query, handled)
		}
	}
}

func TestStreamAgentSearchReturnsParentSynthesis(t *testing.T) {
	var received subagent.ExternalSearchInput
	search := func(_ context.Context, input subagent.ExternalSearchInput) (subagent.ExternalSearchResult, error) {
		received = input
		return subagent.ExternalSearchResult{Agent: "grok", Summary: "Evidence report"}, nil
	}
	toolRuntime := testAgentSearchRuntime(t, search)
	parentClient := &fakeClient{}
	events := make(chan agentEvent, 8)
	streamAgentSearch(t.Context(), toolRuntime, "latest API behavior", "search-call-1", agentSearchParent{
		client: parentClient, model: "main-model", tools: toolRuntime,
		reasoningEffort: "high",
	}, events)

	var activities []agentActivity
	var responseText string
	for event := range events {
		if event.activity != nil {
			activities = append(activities, *event.activity)
		}
		if event.response != nil {
			responseText = event.response.Choices[0].Message.Content
		}
	}
	if received.Query != "latest API behavior" || len(received.CompletionCriteria) == 0 ||
		len(activities) != 2 || activities[0].Action != subagent.ProgressStarted ||
		activities[1].Action != subagent.ProgressCompleted || responseText != "reply 1" {
		t.Fatalf("input = %#v, activities = %#v, response = %q", received, activities, responseText)
	}
	if len(parentClient.requests) != 1 ||
		parentClient.requests[0].ReasoningEffort != "high" ||
		!strings.Contains(parentClient.requests[0].Messages[len(parentClient.requests[0].Messages)-1].Content, "Evidence report") ||
		!strings.Contains(parentClient.requests[0].Messages[len(parentClient.requests[0].Messages)-1].Content, "loom_ref") {
		t.Fatalf("parent requests = %#v", parentClient.requests)
	}
	messages := parentClient.requests[0].Messages
	assistant, tool := messages[len(messages)-2], messages[len(messages)-1]
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "search-call-1" ||
		tool.Role != client.RoleTool || tool.ToolCallID != "search-call-1" {
		t.Fatalf("forced tool lifecycle = %#v", messages)
	}
}

func TestAgentSearchParentResponseIsAddedToTUITranscript(t *testing.T) {
	parentClient := &fakeClient{}
	toolRuntime := testAgentSearchRuntime(t, func(_ context.Context, _ subagent.ExternalSearchInput) (subagent.ExternalSearchResult, error) {
		return subagent.ExternalSearchResult{Agent: "codex", Summary: "Sourced evidence"}, nil
	})
	events := make(chan agentEvent, 8)
	streamAgentSearch(t.Context(), toolRuntime, "explain the evidence", "search-call-2", agentSearchParent{
		client: parentClient, model: "main-model", tools: toolRuntime,
	}, events)

	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(config.Default(), parentClient)
	m.beginTurn()
	m.waiting = true
	user := client.Message{Role: client.RoleUser, Content: "explain the evidence"}
	m.messages = append(m.messages, user)
	m.memory.Append(user)
	for event := range events {
		updated, _ := m.Update(agentEventMsg{event: event})
		m = updated.(model)
	}
	last := m.messages[len(m.messages)-1]
	if m.waiting || last.Role != client.RoleAssistant ||
		last.Content != "reply 1" || !strings.Contains(ansi.Strip(m.viewport.View()), "reply 1") {
		t.Fatalf("waiting=%v messages=%#v transcript=%q", m.waiting, m.messages, m.viewport.View())
	}
}

func TestSendAgentSearchUsesDefaultReasoningEffort(t *testing.T) {
	parentClient := &fakeClient{}
	toolRuntime := testAgentSearchRuntime(t, func(_ context.Context, _ subagent.ExternalSearchInput) (subagent.ExternalSearchResult, error) {
		return subagent.ExternalSearchResult{Agent: "codex", Summary: "Sourced evidence"}, nil
	})
	value := config.Default()
	value.Provider.Model, value.Provider.ReasoningEffort = "main-model", "high"
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, parentClient)
	message := m.sendAgentSearch(toolRuntime, "explain the evidence")().(agentEventMsg)
	if message.event.err != nil {
		t.Fatal(message.event.err)
	}
	for event := range message.events {
		if event.err != nil {
			t.Fatal(event.err)
		}
	}
	if len(parentClient.requests) != 1 || parentClient.requests[0].ReasoningEffort != "high" {
		t.Fatalf("parent requests = %#v", parentClient.requests)
	}
}

func testAgentSearchRuntime(t *testing.T, search subagent.ExternalSearchFunc) agentToolRuntime {
	t.Helper()
	base := &fakeAgentTools{}
	invocation, err := subagent.NewInvocationRuntime(base, configuredInvocationCapture(base), subagent.Invocation{
		Tool: subagent.ExternalSearchTool(),
		Source: subagent.InvocationSource{
			Protocol: "acp", Name: "test-search", Kind: "agent-result",
		},
		Handler: func(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
			input, err := subagent.ParseExternalSearchInput(call.Function.Arguments)
			if err != nil {
				return client.ToolResult{}, err
			}
			result, err := search(ctx, input)
			if err != nil {
				return client.ToolResult{}, err
			}
			body, err := json.Marshal(result)
			return client.ToolResult{Content: string(body)}, err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &agentInvocationToolRuntime{base: base, invocation: invocation}
}

func TestAgentSearchWithoutAssignmentShowsConfigurationHint(t *testing.T) {
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(config.Default(), &fakeClient{})
	m.input.SetValue("/agent:search current API behavior")
	updated, command := m.submitChat()
	m = updated.(model)
	if command != nil || !strings.Contains(m.status, "Search agent is not configured") {
		t.Fatalf("command = %v, status = %q", command, m.status)
	}
}

func TestDefaultAgentCanCallCapturedExternalSearch(t *testing.T) {
	toolRuntime := testAgentSearchRuntime(t, func(_ context.Context, input subagent.ExternalSearchInput) (subagent.ExternalSearchResult, error) {
		return subagent.ExternalSearchResult{Agent: "codex", Summary: "Evidence for " + input.Query}, nil
	})
	configuredClient := &externalSearchCallingClient{}
	events := make(chan agentEvent, 8)
	streamAgentLoop(
		t.Context(), configuredClient, toolRuntime, "main-model", "high",
		[]client.Message{{Role: client.RoleUser, Content: "What changed?"}}, "", nil, false, false, events,
	)
	var response string
	for event := range events {
		if event.response != nil {
			response = event.response.Choices[0].Message.Content
		}
	}
	if response != "Synthesized answer" || len(configuredClient.requests) != 2 {
		t.Fatalf("response=%q requests=%#v", response, configuredClient.requests)
	}
	for _, request := range configuredClient.requests {
		if request.ReasoningEffort != "high" {
			t.Fatalf("tool continuation effort = %q", request.ReasoningEffort)
		}
	}
	toolMessage := configuredClient.requests[1].Messages[len(configuredClient.requests[1].Messages)-1]
	if toolMessage.Role != client.RoleTool || toolMessage.ToolCallID != "search-1" ||
		!strings.Contains(toolMessage.Content, "loom_ref") || !strings.Contains(toolMessage.Content, "Evidence for current behavior") {
		t.Fatalf("tool message = %#v", toolMessage)
	}
}

type externalSearchCallingClient struct {
	requests []client.ChatRequest
}

func (c *externalSearchCallingClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	c.requests = append(c.requests, request)
	if len(c.requests) == 1 {
		if !requestHasTool(request.Tools, subagent.ExternalSearchToolName) {
			return nil, errors.New("external_search was not advertised")
		}
		return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
			Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
				ID: "search-1", Type: client.ToolTypeFunction,
				Function: client.FunctionCall{Name: subagent.ExternalSearchToolName, Arguments: `{"query":"current behavior"}`},
			}},
		}}}}, nil
	}
	return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
		Role: client.RoleAssistant, Content: "Synthesized answer",
	}}}}, nil
}

func (c *externalSearchCallingClient) ListModels(context.Context) ([]client.Model, error) {
	return nil, nil
}

func (c *externalSearchCallingClient) Close() error { return nil }

func requestHasTool(tools []client.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}
