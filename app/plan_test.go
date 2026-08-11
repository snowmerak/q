package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
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
	workspaceStore := workspace.Store{Root: t.TempDir()}
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
	m.workspaceStore = &workspaceStore
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
	updated, command = m.Update(nextAgentMessage(t, command))
	m = updated.(model)
	persisted, err := workspaceStore.LoadExecution()
	if err != nil || persisted.ExecutionID == "" || persisted.RunID != m.runID {
		t.Fatalf("approved execution checkpoint = %#v, error = %v", persisted, err)
	}
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
	completedView := ansi.Strip(m.viewChat())
	if m.agentTraceExpanded || strings.Contains(completedView, "SUBAGENT TRACE") ||
		!strings.Contains(completedView, "SUBAGENTS COMPLETE · ctrl+g inspect trace") ||
		!strings.Contains(completedView, "Plan executed successfully.") ||
		!strings.Contains(completedView, "Type a message…") {
		t.Fatalf("completed plan did not collapse trace and reveal result/input:\n%s", completedView)
	}
	if height := lipgloss.Height(completedView); height > m.height {
		t.Fatalf("completed plan view height %d exceeds terminal %d", height, m.height)
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
	firstReviewInput := configuredClient.requests[6].Messages[1].Content
	if !strings.Contains(firstReviewInput, `"evidence"`) ||
		!strings.Contains(firstReviewInput, `"tool": "write_file"`) ||
		!strings.Contains(firstReviewInput, `"loom_ref": "loom://0123456789abcdef0123456789abcdef"`) ||
		!strings.Contains(firstReviewInput, `"paths": [`) || !strings.Contains(firstReviewInput, `"app/plan.go"`) {
		t.Fatalf("Planner review did not receive bounded Coder evidence: %s", firstReviewInput)
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
	updated, _ = m.updateChatKey(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	m = updated.(model)
	if !m.agentTraceExpanded || !strings.Contains(ansi.Strip(m.viewChat()), "SUBAGENT TRACE") {
		t.Fatalf("completed trace could not be reopened")
	}
	if _, err := workspaceStore.LoadExecution(); !errors.Is(err, workspace.ErrExecutionNotFound) {
		t.Fatalf("successful plan left an execution checkpoint: %v", err)
	}
}

func TestInterruptedPlanRestoresInspectsAndResumesFromPlannerReview(t *testing.T) {
	workspaceStore := workspace.Store{Root: t.TempDir()}
	checkpoint := resumablePlanCheckpoint(subagent.ExecutionPhaseReviewPending)
	checkpoint.Targets = []string{"app/plan.go"}
	checkpoint.Attempts = 1
	checkpoint.TaskAttempts = 1
	checkpoint.PendingResult = &subagent.CoderResult{
		Outcome: "succeeded", Summary: "Coder already changed the workspace",
		Artifacts: []string{"app/plan.go"}, Verification: []string{"go test ./app"},
	}
	if err := workspaceStore.SaveExecution(checkpoint); err != nil {
		t.Fatal(err)
	}
	configuredClient := &planningClient{responses: []client.Message{{
		Role: client.RoleAssistant,
		ToolCalls: []client.ToolCall{planToolCall(subagent.ReviewTaskToolName, `{
			"decision":"next",
			"feedback":"",
			"facts":["The interrupted Coder result was reviewed after restart"]
		}`)},
	}}}
	value := config.Default()
	value.Provider.Model = "plan-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspaceStore
	m.toolRuntime = &fakeAgentTools{}
	m.enterChat(value, configuredClient)
	m.resize(100, 30)

	recoveryView := ansi.Strip(m.viewChat())
	if !m.planResumePending || !m.asking || m.pendingQuestion.Question != "Resume interrupted plan?" ||
		!strings.Contains(recoveryView, "EXECUTION RECOVERY") {
		t.Fatalf("recovery prompt = pending %v asking %v question %#v\n%s", m.planResumePending, m.asking, m.pendingQuestion, recoveryView)
	}
	if height := lipgloss.Height(recoveryView); height > m.height {
		t.Fatalf("recovery view height %d exceeds terminal %d", height, m.height)
	}
	m.questionChoice = 1
	updated, _ := m.submitChat()
	m = updated.(model)
	if !m.asking || !strings.Contains(m.pendingQuestion.Context, "Saved plan:") ||
		!strings.Contains(m.pendingQuestion.Context, "app/plan.go") {
		t.Fatalf("inspect context = %q", m.pendingQuestion.Context)
	}
	if inspectHeight := lipgloss.Height(ansi.Strip(m.viewChat())); inspectHeight > m.height {
		t.Fatalf("inspected recovery view height %d exceeds terminal %d", inspectHeight, m.height)
	}

	m.input.Reset()
	updated, command := m.submitChat()
	m = updated.(model)
	if !m.waiting || m.planResumePending || command == nil {
		t.Fatalf("resume state = waiting %v pending %v command %v", m.waiting, m.planResumePending, command != nil)
	}
	for attempts := 0; m.waiting && attempts < 64; attempts++ {
		updated, command = m.Update(nextAgentMessage(t, command))
		m = updated.(model)
	}
	if m.waiting || len(m.messages) == 0 || !strings.Contains(m.messages[len(m.messages)-1].Content, "Plan executed successfully") {
		t.Fatalf("resumed plan did not finish: waiting %v messages %#v", m.waiting, m.messages)
	}
	if len(configuredClient.requests) != 1 || !hasPlanTool(configuredClient.requests[0].Tools, subagent.ReviewTaskToolName) {
		t.Fatalf("resume repeated Coder or missed review: %#v", configuredClient.requests)
	}
	if len(m.toolRuntime.(*fakeAgentTools).calls) != 0 {
		t.Fatalf("resume repeated workspace tools: %#v", m.toolRuntime.(*fakeAgentTools).calls)
	}
	if _, err := workspaceStore.LoadExecution(); !errors.Is(err, workspace.ErrExecutionNotFound) {
		t.Fatalf("completed checkpoint still exists: %v", err)
	}
}

func TestInterruptedPlanCanBeDiscardedWithoutExecution(t *testing.T) {
	workspaceStore := workspace.Store{Root: t.TempDir()}
	checkpoint := resumablePlanCheckpoint(subagent.ExecutionPhaseTarget)
	if err := workspaceStore.SaveExecution(checkpoint); err != nil {
		t.Fatal(err)
	}
	configuredClient := &planningClient{}
	value := config.Default()
	value.Provider.Model = "plan-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspaceStore
	m.toolRuntime = &fakeAgentTools{}
	m.enterChat(value, configuredClient)
	m.questionChoice = 2

	updated, _ := m.submitChat()
	m = updated.(model)
	if m.asking || m.planResumePending || !strings.Contains(m.status, "workspace changes were kept") {
		t.Fatalf("discard state = asking %v pending %v status %q", m.asking, m.planResumePending, m.status)
	}
	if len(configuredClient.requests) != 0 {
		t.Fatalf("discard executed model requests: %#v", configuredClient.requests)
	}
	if _, err := workspaceStore.LoadExecution(); !errors.Is(err, workspace.ErrExecutionNotFound) {
		t.Fatalf("discarded checkpoint still exists: %v", err)
	}
}

func TestInterruptedCoderResumesAsNewRecoveryAttempt(t *testing.T) {
	workspaceStore := workspace.Store{Root: t.TempDir()}
	checkpoint := resumablePlanCheckpoint(subagent.ExecutionPhaseCoderRunning)
	checkpoint.Targets = []string{"app/plan.go"}
	if err := workspaceStore.SaveExecution(checkpoint); err != nil {
		t.Fatal(err)
	}
	configuredClient := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.CoderCompleteToolName, `{
			"outcome":"succeeded",
			"summary":"Inspected and completed the interrupted workspace change",
			"verification":["go test ./app"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.ReviewTaskToolName, `{
			"decision":"next",
			"feedback":""
		}`)}},
	}}
	value := config.Default()
	value.Provider.Model = "plan-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspaceStore
	m.toolRuntime = &fakeAgentTools{}
	m.enterChat(value, configuredClient)

	updated, command := m.submitChat()
	m = updated.(model)
	for attempts := 0; m.waiting && attempts < 64; attempts++ {
		updated, command = m.Update(nextAgentMessage(t, command))
		m = updated.(model)
	}
	if m.waiting || len(configuredClient.requests) != 2 {
		t.Fatalf("recovery execution = waiting %v requests %#v", m.waiting, configuredClient.requests)
	}
	coderPrompt := configuredClient.requests[0].Messages[0].Content
	if !strings.Contains(coderPrompt, `"attempt": 2`) ||
		!strings.Contains(coderPrompt, "previous Coder attempt was interrupted") ||
		!strings.Contains(coderPrompt, "Inspect the current workspace") {
		t.Fatalf("recovery Coder prompt = %s", coderPrompt)
	}
	if _, err := workspaceStore.LoadExecution(); !errors.Is(err, workspace.ErrExecutionNotFound) {
		t.Fatalf("recovered checkpoint still exists: %v", err)
	}
}

func TestInterruptImmediatelyOffersDurablePlanRecovery(t *testing.T) {
	workspaceStore := workspace.Store{Root: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "plan-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspaceStore
	m.toolRuntime = &fakeAgentTools{}
	m.enterChat(value, &planningClient{})
	checkpoint := resumablePlanCheckpoint(subagent.ExecutionPhaseTarget)
	checkpoint.RunID = m.runID
	if err := workspaceStore.SaveExecution(checkpoint); err != nil {
		t.Fatal(err)
	}
	m.beginTurn()
	m.waiting = true
	m.status = "Coder running"

	updated, _ := m.interruptTurn()
	m = updated.(model)
	if m.waiting || !m.asking || !m.planResumePending || m.pendingQuestion.Question != "Resume interrupted plan?" {
		t.Fatalf("interrupt recovery = waiting %v asking %v pending %v question %#v", m.waiting, m.asking, m.planResumePending, m.pendingQuestion)
	}
}

func resumablePlanCheckpoint(phase subagent.ExecutionPhase) subagent.ExecutionCheckpoint {
	return subagent.ExecutionCheckpoint{
		ExecutionID: "plan-execution-resume", RunID: "run-resume", Objective: "Persist approved execution",
		Phase: phase, Attempt: 1,
		Plan: subagent.PlanProposal{
			Outcome: "succeeded", Summary: "Resume an approved plan",
			Conditions: []string{"Do not repeat completed side effects"},
			Steps: []subagent.PlanStep{{
				Title: "Connect persistence", Description: "Resume the saved task",
				Target: subagent.TargetCondition{Any: []subagent.TargetProduct{{All: []subagent.TargetSelector{{
					Kind: subagent.TargetSelectorPaths, Paths: []string{"app/plan.go"},
				}}}}},
			}},
		},
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
