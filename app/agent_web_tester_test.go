package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

func TestParseAgentWebTesterCommand(t *testing.T) {
	for _, test := range []struct {
		command string
		request string
		handled bool
	}{
		{command: "/agent:web-tester", handled: true},
		{command: "/agent:web-tester verify login", request: "verify login", handled: true},
		{command: "/agent:web-tester-other", handled: false},
	} {
		request, handled := parseAgentWebTesterCommand(test.command)
		if request != test.request || handled != test.handled {
			t.Fatalf("parseAgentWebTesterCommand(%q) = %q, %v", test.command, request, handled)
		}
	}
}

func TestAgentWebTesterWithoutAssignmentIsNotDiscoveredOrHandledAsCommand(t *testing.T) {
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(config.Default(), &fakeClient{})
	for _, command := range m.slashCommands() {
		if command.name == agentWebTesterCommand {
			t.Fatal("unconfigured web tester command was discovered")
		}
	}
	m.input.SetValue("/agent:web-tester verify login")
	updated, command := m.submitChat()
	m = updated.(model)
	if command == nil || m.status != "Thinking…" {
		t.Fatalf("command=%v status=%q", command, m.status)
	}
}

func TestStreamAgentWebTesterReturnsParentSynthesis(t *testing.T) {
	var received subagent.ExternalWebTesterInput
	runtime := testAgentWebTesterRuntime(t, func(_ context.Context, input subagent.ExternalWebTesterInput) (subagent.ExternalWebTesterResult, error) {
		received = input
		return subagent.ExternalWebTesterResult{Agent: "browser", Outcome: "succeeded", Summary: "login verified"}, nil
	})
	parentClient := &fakeClient{}
	events := make(chan agentEvent, 8)
	streamAgentWebTester(t.Context(), runtime, "verify login", "web-call-1", agentSearchParent{
		client: parentClient, tools: runtime, model: "main-model", reasoningEffort: "high",
	}, events)
	var activities []agentActivity
	var response string
	for event := range events {
		if event.activity != nil {
			activities = append(activities, *event.activity)
		}
		if event.response != nil {
			response = event.response.Choices[0].Message.Content
		}
	}
	if received.Request != "verify login" || len(received.CompletionCriteria) == 0 ||
		len(activities) != 2 || response != "reply 1" {
		t.Fatalf("input=%#v activities=%#v response=%q", received, activities, response)
	}
	messages := parentClient.requests[0].Messages
	assistant, tool := messages[len(messages)-2], messages[len(messages)-1]
	if assistant.ToolCalls[0].Function.Name != subagent.ExternalWebTesterToolName ||
		assistant.ToolCalls[0].ID != "web-call-1" || tool.ToolCallID != "web-call-1" ||
		!strings.Contains(tool.Content, "loom_ref") {
		t.Fatalf("forced tool lifecycle = %#v", messages)
	}
}

func TestACPAgentRunsExplicitWebTesterCommand(t *testing.T) {
	parentClient := &fakeClient{}
	agent, workspaceStore, connection := testACPAgent(t, parentClient, &fakeAgentTools{})
	var received subagent.ExternalWebTesterInput
	agent.state.toolRuntime = testAgentWebTesterRuntime(t, func(_ context.Context, input subagent.ExternalWebTesterInput) (subagent.ExternalWebTesterResult, error) {
		received = input
		return subagent.ExternalWebTesterResult{Agent: "browser", Outcome: "succeeded", Summary: "login verified"}, nil
	})
	agent.state.config.Agents.Connections = map[string]config.AgentConnectionConfig{"browser": {Preset: "codex"}}
	agent.state.config.Agents.Roles = map[string]config.AgentConfig{
		config.AgentRoleExternalWebTester: {Agent: "browser"},
	}
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock("/agent:web-tester verify login")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonEndTurn || received.Request != "verify login" || len(parentClient.requests) != 1 {
		t.Fatalf("response=%#v input=%#v requests=%#v", response, received, parentClient.requests)
	}
	var output string
	for _, notification := range connection.snapshot() {
		if update := notification.Update.AgentMessageChunk; update != nil && update.Content.Text != nil {
			output += update.Content.Text.Text
		}
	}
	if output != "reply 1" {
		t.Fatalf("output = %q", output)
	}
}

func TestExternalWebTesterPlanAdapterCapturesRawResultAndReturnsCommonResult(t *testing.T) {
	var received subagent.ExternalWebTesterInput
	invocation := subagent.Invocation{
		Tool:   subagent.ExternalWebTesterTool(),
		Source: subagent.InvocationSource{Protocol: "acp", Name: "browser", Kind: "agent-result"},
		Handler: func(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
			var err error
			received, err = subagent.ParseExternalWebTesterInput(call.Function.Arguments)
			if err != nil {
				return client.ToolResult{}, err
			}
			return client.ToolResult{Content: `{"agent":"ignored","outcome":"failed","summary":"cookie missing","verification":["login submitted"]}`}, nil
		},
	}
	captured := false
	runner := externalWebTesterTaskRunner(invocation, func(
		_ context.Context, _ subagent.InvocationSource, _ client.ToolCall, result client.ToolResult,
	) (client.ToolResult, error) {
		captured = strings.Contains(result.Content, "cookie missing")
		return client.ToolResult{Content: `{"loom_ref":"loom://0123456789abcdef0123456789abcdef","stored":true}`}, nil
	})
	plan := subagent.PlanProposal{Summary: "verify auth", Facts: []string{"local app"}, Verification: []string{"auth works"}, Steps: []subagent.PlanStep{{
		Title: "Test login", Description: "Use the browser", Executor: subagent.PlanExecutorExternalWebTester,
		Verification: []string{"dashboard appears"},
	}}}
	result, err := runner(t.Context(), subagent.TaskAttempt{
		Plan: plan, TaskIndex: 0, Attempt: 2, Executor: subagent.PlanExecutorExternalWebTester,
		Targets: []string{"web/login.go"}, Feedback: "retest the fix",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !captured || received.Request != "Test login\n\nUse the browser" ||
		!strings.Contains(strings.Join(received.Context, "\n"), "retest the fix") ||
		result.Executor != subagent.PlanExecutorExternalWebTester || result.Outcome != "failed" ||
		len(result.Evidence) != 1 || result.Evidence[0].LoomRef == "" {
		t.Fatalf("captured=%v input=%#v result=%#v", captured, received, result)
	}
}

func testAgentWebTesterRuntime(
	t *testing.T,
	run func(context.Context, subagent.ExternalWebTesterInput) (subagent.ExternalWebTesterResult, error),
) agentToolRuntime {
	t.Helper()
	base := &fakeAgentTools{}
	invocation, err := subagent.NewInvocationRuntime(base, configuredInvocationCapture(base), subagent.Invocation{
		Tool:   subagent.ExternalWebTesterTool(),
		Source: subagent.InvocationSource{Protocol: "acp", Name: "test-browser", Kind: "agent-result"},
		Handler: func(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
			input, err := subagent.ParseExternalWebTesterInput(call.Function.Arguments)
			if err != nil {
				return client.ToolResult{}, err
			}
			result, err := run(ctx, input)
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
