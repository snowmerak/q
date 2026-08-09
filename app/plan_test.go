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

func TestPlanCommandRunsGrillerPlannerAndStopsAfterApproval(t *testing.T) {
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
			"conditions":["Stop before execution"],
			"acceptance_criteria":["The user approves the composed plan"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitPlanToolName, `{
			"outcome":"succeeded",
			"summary":"Connect an approval-gated plan flow",
			"conditions":["Stop before execution"],
			"steps":[{"title":"Connect roles","description":"Run Griller and Planner in sequence","target":{"any":[{"all":[{"kind":"paths","paths":["app/plan.go"]}]}]}}],
			"verification":["Complete one approved plan cycle"]
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
		!strings.Contains(m.pendingQuestion.Context, "Connect an approval-gated plan flow") {
		t.Fatalf("confirmation state = asking %v question %#v", m.asking, m.pendingQuestion)
	}
	view := ansi.Strip(m.viewChat())
	for _, expected := range []string{"agents", "griller ✓", "scout ✓", "planner ✓", "submit_plan", "› approve · Approve", "Type a custom answer"} {
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
	for attempts := 0; m.waiting && attempts < 64; attempts++ {
		updated, command = m.Update(nextAgentMessage(t, command))
		m = updated.(model)
	}
	if m.waiting || len(m.messages) == 0 {
		t.Fatalf("plan did not finish: waiting %v messages %#v", m.waiting, m.messages)
	}
	final := m.messages[len(m.messages)-1].Content
	if !strings.Contains(final, "Plan approved. Execution has not started.") ||
		!strings.Contains(final, "Connect an approval-gated plan flow") {
		t.Fatalf("final plan = %q", final)
	}
	if len(configuredClient.requests) != 4 {
		t.Fatalf("planning requests = %#v", configuredClient.requests)
	}
	for _, request := range configuredClient.requests {
		if request.Model != "plan-model" {
			t.Fatalf("planning request model = %#v", request)
		}
	}
}

func planToolCall(name, arguments string) client.ToolCall {
	return client.ToolCall{
		ID: "call-" + name, Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: name, Arguments: arguments},
	}
}
