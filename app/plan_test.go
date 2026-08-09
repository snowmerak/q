package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
)

type planningClient struct {
	mu        sync.Mutex
	responses []client.Message
	requests  []client.ChatRequest
}

func (p *planningClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, request)
	if len(p.responses) == 0 {
		return nil, errors.New("no planning response")
	}
	message := p.responses[0]
	p.responses = p.responses[1:]
	return &client.ChatResponse{Choices: []client.Choice{{Message: message}}}, nil
}

func (p *planningClient) ListModels(context.Context) ([]client.Model, error) {
	return []client.Model{{ID: "plan-model"}}, nil
}

func (p *planningClient) Close() error { return nil }

func TestPlanCommandExecutesApprovedPlanWithCoderAndPlannerReview(t *testing.T) {
	configuredClient := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.DelegateScoutToolName, `{
			"objective":"Locate the plan command boundary"
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.ScoutCompleteToolName, `{
			"outcome":"succeeded",
			"summary":"Located the plan command boundary"
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitBriefToolName, `{
			"objective":"Add a plan flow",
			"conditions":["Execute only after approval"],
			"acceptance_criteria":["The approved task is implemented and reviewed"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitPlanToolName, `{
			"outcome":"succeeded",
			"summary":"Connect and execute an approval-gated plan flow",
			"conditions":["Execute only after approval"],
			"steps":[{"title":"Connect roles","description":"Run Griller and Planner in sequence","target":{"any":[{"all":[{"kind":"paths","paths":["app/plan.go"]}]}]}}],
			"verification":["Complete one approved plan cycle"]
		}`)}},
		{Role: client.RoleAssistant, Content: "I will update the approved execution boundary, then verify it.", ToolCalls: []client.ToolCall{planToolCall("write_file", `{"path":"app/plan.go","content":"updated"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.CoderCompleteToolName, `{
			"outcome":"succeeded",
			"summary":"Connected the approved execution flow",
			"artifacts":["app/plan.go"],
			"verification":["go test ./app"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.ReviewTaskToolName, `{
			"decision":"retry",
			"feedback":"Preserve the existing command boundary",
			"facts":["app/plan.go owns the approved execution boundary"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.CoderCompleteToolName, `{
			"outcome":"succeeded",
			"summary":"Preserved the command boundary and connected execution",
			"artifacts":["app/plan.go"],
			"verification":["go test ./app"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.ReviewTaskToolName, `{
			"decision":"next",
			"feedback":"",
			"facts":["The approved plan now enters the Coder loop"]
		}`)}},
	}}
	value := config.Default()
	value.Provider.Model = "plan-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.toolRuntime = &fakeAgentTools{}
	m.enterChat(value, configuredClient)
	m.resize(100, 36)

	m.input.SetValue("/plan")
	updated, _ := m.submitChat()
	m = updated.(model)
	if !m.planArmed || m.waiting || !strings.Contains(m.input.Placeholder, "work to plan") {
		t.Fatalf("armed state = %#v", m)
	}

	m.input.SetValue("Add a plan flow")
	updated, command := m.submitChat()
	m = updated.(model)
	if m.planArmed || !m.waiting || command == nil {
		t.Fatalf("started state = armed %v waiting %v command %v", m.planArmed, m.waiting, command != nil)
	}
	for attempts := 0; !m.asking && attempts < 64; attempts++ {
		updated, command = m.Update(nextAgentMessage(t, command))
		m = updated.(model)
	}
	if !m.asking || m.pendingQuestion.Question != "Approve this plan?" ||
		!strings.Contains(m.pendingQuestion.Context, "Connect and execute an approval-gated plan flow") {
		t.Fatalf("confirmation state = asking %v question %#v", m.asking, m.pendingQuestion)
	}
	view := ansi.Strip(m.viewChat())
	for _, expected := range []string{"SUBAGENT TRACE", "agents", "griller ✓", "scout ✓", "planner ✓", "submit_plan", "PLAN APPROVAL", "› approve · Approve", "Type a custom answer"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("agent activity panel does not contain %q:\n%s", expected, view)
		}
	}
	if height := strings.Count(view, "\n") + 1; height > m.height {
		t.Fatalf("plan confirmation view height %d exceeds terminal height %d", height, m.height)
	}

	m.input.Reset()
	updated, command = m.submitChat()
	m = updated.(model)
	for attempts := 0; m.waiting && attempts < 128; attempts++ {
		updated, command = m.Update(nextAgentMessage(t, command))
		m = updated.(model)
	}
	if m.waiting || len(m.messages) == 0 {
		t.Fatalf("plan did not finish: waiting %v messages %#v", m.waiting, m.messages)
	}
	final := m.messages[len(m.messages)-1].Content
	if !strings.Contains(final, "Plan executed successfully.") ||
		!strings.Contains(final, "Preserved the command boundary and connected execution") ||
		!strings.Contains(final, "attempts: 2") ||
		!strings.Contains(final, "go test ./app") {
		t.Fatalf("final execution = %q", final)
	}
	if len(configuredClient.requests) != 9 {
		t.Fatalf("planning requests = %#v", configuredClient.requests)
	}
	for _, request := range configuredClient.requests {
		if request.Model != "plan-model" {
			t.Fatalf("planning request model = %#v", request)
		}
	}
	if len(m.toolRuntime.(*fakeAgentTools).calls) != 1 || m.toolRuntime.(*fakeAgentTools).calls[0].Function.Name != "write_file" {
		t.Fatalf("Coder workspace calls = %#v", m.toolRuntime.(*fakeAgentTools).calls)
	}
	if !hasPlanTool(configuredClient.requests[4].Tools, "write_file") ||
		!hasPlanTool(configuredClient.requests[4].Tools, subagent.CoderCompleteToolName) ||
		!strings.Contains(configuredClient.requests[4].Messages[0].Content, `"resolved_targets"`) {
		t.Fatalf("Coder request = %#v", configuredClient.requests[4])
	}
	if !hasPlanTool(configuredClient.requests[6].Tools, subagent.ReviewTaskToolName) ||
		!hasPlanTool(configuredClient.requests[8].Tools, subagent.ReviewTaskToolName) {
		t.Fatalf("Planner review requests = %#v / %#v", configuredClient.requests[6], configuredClient.requests[8])
	}
	retryPrompt := configuredClient.requests[7].Messages[0].Content
	if !strings.Contains(retryPrompt, "Preserve the existing command boundary") ||
		!strings.Contains(retryPrompt, "app/plan.go owns the approved execution boundary") ||
		!strings.Contains(retryPrompt, `"attempt": 2`) {
		t.Fatalf("Coder retry prompt = %s", retryPrompt)
	}
	var sawCoderTool, sawPlannerReview, sawExecutionComplete bool
	for _, activity := range m.agentActivities {
		if activity.Agent == "coder" && activity.Action == subagent.ProgressTool && activity.Detail == "write_file" {
			sawCoderTool = true
		}
		if activity.Agent == "planner" && activity.Action == subagent.ProgressTool && activity.Detail == subagent.ReviewTaskToolName {
			sawPlannerReview = true
		}
		if activity.Agent == "executor" && activity.Action == subagent.ProgressCompleted && activity.Detail == "approved plan executed" {
			sawExecutionComplete = true
		}
	}
	if !sawCoderTool || !sawPlannerReview || !sawExecutionComplete {
		t.Fatalf("execution activity missing: %#v", m.agentActivities)
	}
	var sawAssistant, sawToolCall, sawToolResult, sawReviewPayload bool
	for _, trace := range m.agentTraces {
		if trace.Agent == "coder" && trace.Kind == subagent.TraceAssistant && strings.Contains(trace.Content, "approved execution boundary") {
			sawAssistant = true
		}
		if trace.Agent == "coder" && trace.Kind == subagent.TraceToolCall && trace.Name == "write_file" && strings.Contains(trace.Content, `"app/plan.go"`) {
			sawToolCall = true
		}
		if trace.Agent == "coder" && trace.Kind == subagent.TraceToolResult && trace.Name == "write_file" && strings.Contains(trace.Content, "loom_ref") {
			sawToolResult = true
		}
		if trace.Agent == "planner" && trace.Kind == subagent.TraceToolCall && trace.Name == subagent.ReviewTaskToolName && strings.Contains(trace.Content, `"retry"`) {
			sawReviewPayload = true
		}
	}
	if !sawAssistant || !sawToolCall || !sawToolResult || !sawReviewPayload {
		t.Fatalf("detailed execution trace missing: %#v", m.agentTraces)
	}
}

func planToolCall(name, arguments string) client.ToolCall {
	return client.ToolCall{
		ID: "call-" + name, Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: name, Arguments: arguments},
	}
}

func hasPlanTool(tools []client.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}
