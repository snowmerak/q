package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	mu               sync.Mutex
	responses        []client.Message
	requests         []client.ChatRequest
	terminalRequests []client.ChatRequest
}

func (p *planningClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if request.ToolChoice == client.ToolChoiceNone && len(request.Messages) > 0 && request.Messages[len(request.Messages)-1].Role == client.RoleTool {
		p.terminalRequests = append(p.terminalRequests, request)
		return terminalAcknowledgment(""), nil
	}
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
	value.Provider.Model = "global-model"
	value.Agents.Connections = map[string]config.AgentConnectionConfig{"search-agent": {Preset: "codex"}}
	value.Agents.Roles = map[string]config.AgentConfig{config.AgentRoleSearch: {Agent: "search-agent"}}
	if err := workspaceStore.SaveModelConfig(workspace.ModelConfig{Overrides: map[string]workspace.ModelOverride{
		defaultModelTarget: {Model: "plan-model"},
	}}); err != nil {
		t.Fatal(err)
	}
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
	for _, index := range []int{0, 3, 6, 8} {
		if !hasPlanTool(configuredClient.requests[index].Tools, subagent.ExternalSearchToolName) {
			t.Fatalf("parent/reviewer request %d is missing external_search", index)
		}
	}
	for _, index := range []int{4, 7} {
		if hasPlanTool(configuredClient.requests[index].Tools, subagent.ExternalSearchToolName) {
			t.Fatalf("Coder request %d unexpectedly received external_search", index)
		}
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
	assertArchivedPlanCheckpoint(t, workspaceStore, 1)
	assertArchivedPlanningAudit(t, workspaceStore, m.runID)
	assertArchivedExecutionAuditIncludesFinalTask(t, workspaceStore)
}

func assertArchivedPlanningAudit(t *testing.T, store workspace.Store, runID string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(store.ExecutionHistoryDir(), "plan-execution-*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("execution logs=%v, err=%v", paths, err)
	}
	body, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Checkpoint subagent.ExecutionCheckpoint `json:"checkpoint"`
	}
	if err := json.Unmarshal(body, &stored); err != nil {
		t.Fatal(err)
	}
	planning := stored.Checkpoint.Planning
	if planning == nil || planning.RunID != runID || planning.Outcome != subagent.PlanningOutcomeApproved ||
		planning.Cycles != 1 || planning.Brief == nil || planning.Plan == nil ||
		planning.Brief.Objective != "Add a plan flow" || !strings.Contains(planning.Plan.Summary, "approval-gated") ||
		planning.StartedAt.IsZero() || planning.CompletedAt.Before(planning.StartedAt) {
		t.Fatalf("incomplete planning audit: %s", body)
	}
	wantTypes := map[string]bool{
		subagent.PlanningEventInput: false, subagent.PlanningEventActivity: false, subagent.PlanningEventToolCall: false,
		subagent.PlanningEventToolResult: false, subagent.PlanningEventQuestion: false,
		subagent.PlanningEventAnswer: false,
	}
	var sawGriller, sawScout, sawPlanner, sawDelegate, sawScoutResult, sawApproval bool
	for index, event := range planning.Events {
		if event.Sequence != index+1 || event.At.Before(planning.StartedAt) || event.At.After(planning.CompletedAt) {
			t.Fatalf("planning event is out of order: %#v", event)
		}
		if _, tracked := wantTypes[event.Type]; tracked {
			wantTypes[event.Type] = true
		}
		sawGriller = sawGriller || event.Agent == "griller" && event.TaskID == "grill-"+runID+"-griller-1"
		sawScout = sawScout || event.Agent == "scout"
		sawPlanner = sawPlanner || event.Agent == "planner" && event.TaskID == "grill-"+runID+"-planner-1"
		if event.Type == subagent.PlanningEventToolCall && event.Name == subagent.DelegateScoutToolName {
			sawDelegate = strings.Contains(event.Content, "Locate the plan command boundary")
		}
		if event.Type == subagent.PlanningEventToolResult && event.Name == subagent.DelegateScoutToolName {
			sawScoutResult = strings.Contains(event.Content, "Located the plan command boundary")
		}
		if event.Type == subagent.PlanningEventAnswer && event.Answer != nil && event.Answer.SelectedChoiceID == "approve" {
			sawApproval = true
		}
		if event.Agent == "coder" || event.Agent == "executor" {
			t.Fatalf("execution event leaked into planning audit: %#v", event)
		}
	}
	for eventType, found := range wantTypes {
		if !found {
			t.Fatalf("planning audit omitted %q event: %#v", eventType, planning.Events)
		}
	}
	if !sawGriller || !sawScout || !sawPlanner || !sawDelegate || !sawScoutResult || !sawApproval {
		t.Fatalf("planning audit omitted visible planning work: %#v", planning.Events)
	}
}

func assertArchivedExecutionAuditIncludesFinalTask(t *testing.T, store workspace.Store) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(store.ExecutionHistoryDir(), "plan-execution-*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("execution logs=%v, err=%v", paths, err)
	}
	body, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Checkpoint subagent.ExecutionCheckpoint `json:"checkpoint"`
	}
	if err := json.Unmarshal(body, &stored); err != nil {
		t.Fatal(err)
	}
	execution := stored.Checkpoint.ExecutionLog
	if execution == nil || execution.StartedAt.IsZero() || execution.CompletedAt == nil ||
		execution.CompletedAt.Before(execution.StartedAt) || len(execution.Events) == 0 {
		t.Fatalf("execution audit is incomplete: %s", body)
	}
	var sawFinalCoderResult, sawFinalReview, sawFinalReviewCompleted bool
	for index, event := range execution.Events {
		if event.Sequence != index+1 || event.At.Before(execution.StartedAt) || event.At.After(*execution.CompletedAt) {
			t.Fatalf("execution event is out of order: %#v", event)
		}
		if event.Agent == "coder" && strings.HasSuffix(event.TaskID, "-coder-1-2") &&
			event.Type == subagent.PlanningEventToolCall && event.Name == subagent.CoderCompleteToolName &&
			strings.Contains(event.Content, "Preserved the command boundary") {
			sawFinalCoderResult = true
		}
		if event.Agent == "planner" && strings.HasSuffix(event.TaskID, "-planner-1-2") &&
			event.Type == subagent.PlanningEventToolCall && event.Name == subagent.ReviewTaskToolName &&
			strings.Contains(event.Content, `"decision":"next"`) {
			sawFinalReview = true
		}
		if event.Agent == "planner" && strings.HasSuffix(event.TaskID, "-planner-1-2") &&
			event.Type == subagent.PlanningEventActivity && event.Action == subagent.ProgressCompleted {
			sawFinalReviewCompleted = true
		}
	}
	if !sawFinalCoderResult || !sawFinalReview || !sawFinalReviewCompleted {
		t.Fatalf("execution audit omitted the final task: %#v", execution.Events)
	}
}

func TestCanceledPlanArchivesReadablePlanningLogWithoutCheckpoint(t *testing.T) {
	workspaceStore := workspace.Store{Root: t.TempDir()}
	configuredClient := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitBriefToolName, `{
			"objective":"Inspect before changing anything",
			"conditions":["Execute only after approval"],
			"acceptance_criteria":["The proposed change is bounded"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitPlanToolName, `{
			"outcome":"succeeded",
			"summary":"A bounded plan that will be canceled",
			"conditions":["Execute only after approval"],
			"steps":[{"title":"Do not execute","description":"Wait for approval","target":{"any":[{"all":[{"kind":"paths","paths":["app/plan.go"]}]}]}}],
			"verification":["No work runs without approval"]
		}`)}},
	}}
	value := config.Default()
	value.Provider.Model = "plan-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspaceStore
	m.toolRuntime = &fakeAgentTools{}
	m.enterChat(value, configuredClient)

	m.input.SetValue("/plan")
	updated, _ := m.submitChat()
	m = updated.(model)
	m.input.SetValue("Inspect before changing anything")
	updated, command := m.submitChat()
	m = updated.(model)
	if command == nil {
		t.Fatalf("canceled plan did not start: armed=%v waiting=%v status=%q client=%v tools=%v input=%q", m.planArmed, m.waiting, m.status, m.client != nil, m.toolRuntime != nil, m.input.Value())
	}
	for attempts := 0; !m.asking && m.waiting && attempts < 64; attempts++ {
		updated, command = m.Update(nextAgentMessage(t, command))
		m = updated.(model)
	}
	if !m.asking || m.pendingQuestion.Question != "Approve this plan?" {
		t.Fatalf("plan did not reach approval: status=%q messages=%#v question=%#v", m.status, m.messages, m.pendingQuestion)
	}
	m.questionChoice = 2
	m.input.Reset()
	updated, command = m.submitChat()
	m = updated.(model)
	for attempts := 0; m.waiting && attempts < 64; attempts++ {
		updated, command = m.Update(nextAgentMessage(t, command))
		m = updated.(model)
	}
	if m.waiting || len(m.messages) == 0 || !strings.Contains(m.messages[len(m.messages)-1].Content, "Planning canceled") {
		t.Fatalf("canceled plan did not finish: waiting=%v messages=%#v", m.waiting, m.messages)
	}
	if _, err := workspaceStore.LoadExecution(); !errors.Is(err, workspace.ErrExecutionNotFound) {
		t.Fatalf("canceled Grill created a recovery checkpoint: %v", err)
	}
	paths, err := filepath.Glob(filepath.Join(workspaceStore.ExecutionHistoryDir(), "plan-planning-*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("planning logs=%v, err=%v", paths, err)
	}
	body, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Planning subagent.PlanningLog `json:"planning"`
	}
	if err := json.Unmarshal(body, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Planning.Outcome != subagent.PlanningOutcomeCanceled || stored.Planning.Plan == nil || stored.Planning.Brief == nil {
		t.Fatalf("canceled planning log is incomplete: %s", body)
	}
	var sawCancel bool
	for _, event := range stored.Planning.Events {
		if event.Type == subagent.PlanningEventAnswer && event.Answer != nil && event.Answer.SelectedChoiceID == "cancel" {
			sawCancel = true
		}
	}
	if !sawCancel {
		t.Fatalf("canceled planning log omitted the user decision: %s", body)
	}
}

func TestFailedPlanArchivesPartialPlanningLogWithoutCheckpoint(t *testing.T) {
	workspaceStore := workspace.Store{Root: t.TempDir()}
	configuredClient := &planningClient{}
	value := config.Default()
	value.Provider.Model = "plan-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspaceStore
	m.toolRuntime = &fakeAgentTools{}
	m.enterChat(value, configuredClient)

	m.input.SetValue("/plan")
	updated, _ := m.submitChat()
	m = updated.(model)
	m.input.SetValue("Record a failed planning attempt")
	updated, command := m.submitChat()
	m = updated.(model)
	for attempts := 0; m.waiting && attempts < 64; attempts++ {
		updated, command = m.Update(nextAgentMessage(t, command))
		m = updated.(model)
	}
	if m.waiting || !strings.Contains(m.status, "no planning response") {
		t.Fatalf("failed plan did not surface its error: waiting=%v status=%q", m.waiting, m.status)
	}
	if _, err := workspaceStore.LoadExecution(); !errors.Is(err, workspace.ErrExecutionNotFound) {
		t.Fatalf("failed Grill created a recovery checkpoint: %v", err)
	}
	paths, err := filepath.Glob(filepath.Join(workspaceStore.ExecutionHistoryDir(), "plan-planning-*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("planning logs=%v, err=%v", paths, err)
	}
	body, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Planning subagent.PlanningLog `json:"planning"`
	}
	if err := json.Unmarshal(body, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Planning.Outcome != subagent.PlanningOutcomeFailed ||
		!strings.Contains(stored.Planning.Error, "no planning response") || len(stored.Planning.Events) == 0 {
		t.Fatalf("failed planning log is incomplete: %s", body)
	}
	var sawFailure bool
	for _, event := range stored.Planning.Events {
		if event.Type == subagent.PlanningEventActivity && event.Agent == "griller" && event.Action == subagent.ProgressFailed {
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Fatalf("failed planning log omitted Griller failure activity: %s", body)
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
	assertArchivedPlanCheckpoint(t, workspaceStore, 1)
}

func TestCompletedPlanRetriesArchivingWithoutRerunningAgents(t *testing.T) {
	store := workspace.Store{Root: t.TempDir()}
	checkpoint := resumablePlanCheckpoint(subagent.ExecutionPhaseCompleted)
	checkpoint.TaskIndex, checkpoint.CompletedTasks, checkpoint.Attempts = 1, 1, 1
	checkpoint.Tasks = []subagent.TaskExecutionResult{{
		TaskIndex: 0, Title: checkpoint.Plan.Steps[0].Title, Attempts: 1,
		Result: subagent.CoderResult{Outcome: "succeeded", Summary: "Already completed"},
	}}
	if err := store.SaveExecution(checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ExecutionHistoryDir(), []byte("history directory unavailable"), 0o600); err != nil {
		t.Fatal(err)
	}
	configuredClient := &planningClient{}
	tools := &fakeAgentTools{}
	value := config.Default()
	value.Provider.Model = "plan-model"
	run := func() error {
		_, err := executeApprovedPlan(t.Context(), configuredClient, tools, value,
			[]client.Model{{ID: "plan-model"}}, nil, checkpoint.RunID, store.Root,
			checkpoint, store, nil, nil, nil, nil)
		return err
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), "history") {
		t.Fatalf("expected archive failure: %v", err)
	}
	if got, err := store.LoadExecution(); err != nil || got.Phase != subagent.ExecutionPhaseCompleted {
		t.Fatalf("archive failure lost completed checkpoint: got=%#v, err=%v", got, err)
	}
	if err := os.Remove(store.ExecutionHistoryDir()); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if len(configuredClient.requests) != 0 || len(tools.calls) != 0 {
		t.Fatalf("archive retry repeated model or tool calls: requests=%d calls=%d", len(configuredClient.requests), len(tools.calls))
	}
	assertArchivedPlanCheckpoint(t, store, 1)
}

func assertArchivedPlanCheckpoint(t *testing.T, store workspace.Store, completedTasks int) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(store.ExecutionHistoryDir(), "plan-execution-*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("execution logs=%v, err=%v", paths, err)
	}
	body, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		SessionID  string                       `json:"session_id"`
		Checkpoint subagent.ExecutionCheckpoint `json:"checkpoint"`
	}
	if err := json.Unmarshal(body, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.SessionID != store.SessionID || stored.Checkpoint.Phase != subagent.ExecutionPhaseCompleted ||
		stored.Checkpoint.CompletedTasks != completedTasks || len(stored.Checkpoint.Tasks) != completedTasks ||
		stored.Checkpoint.ExecutionID == "" || stored.Checkpoint.Objective == "" {
		t.Fatalf("incomplete execution log: %s", body)
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
