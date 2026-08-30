package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

func TestApplyPlanAutomationOverridesUsesOnlyExplicitValues(t *testing.T) {
	configured := config.PlanConfig{AutoApprove: true}
	got := applyPlanAutomationOverrides(configured, PlanAutomationOverrides{
		AutoApprove: BooleanOverride{Set: true, Value: false},
	})
	if got.AutoApprove || got.AutoResolve {
		t.Fatalf("overridden plan config = %#v", got)
	}
	if unchanged := applyPlanAutomationOverrides(configured, PlanAutomationOverrides{}); unchanged != configured {
		t.Fatalf("omitted overrides changed config: %#v", unchanged)
	}
}

func TestEngineeringDefaultResolverProducesConcreteDesignPolicy(t *testing.T) {
	answer, err := engineeringDefaultResolver(t.Context(), subagent.UserQuestion{
		Question: "How should storage be represented?",
		Context:  "The repository currently has one in-memory implementation.",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"How should storage be represented?",
		"smallest stable abstraction or interface",
		"most efficient concrete implementation",
		"do not defer this decision back to the user",
	} {
		if !strings.Contains(answer.Freeform, expected) {
			t.Fatalf("auto-resolve answer omitted %q: %q", expected, answer.Freeform)
		}
	}
	if answer.Source != subagent.UserAnswerSourceAutoResolve || answer.SelectedChoiceID != "" {
		t.Fatalf("auto-resolve answer = %#v", answer)
	}
}

func TestPlanningAuditRecordsAutomaticAnswerSource(t *testing.T) {
	recorder := newPlanningLogRecorder("run", "objective", nil)
	recorder.recordAnswer(subagent.UserAnswer{
		SelectedChoiceID: "approve",
		Source:           subagent.UserAnswerSourceAutoApprove,
	}, nil)
	event := recorder.log.Events[len(recorder.log.Events)-1]
	if event.Agent != subagent.UserAnswerSourceAutoApprove || event.Answer == nil ||
		event.Answer.Source != subagent.UserAnswerSourceAutoApprove {
		t.Fatalf("automatic answer event = %#v", event)
	}
}

func TestACPAgentRunsAutonomousPlanWithoutFormElicitation(t *testing.T) {
	configuredClient := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.AskToUserToolName, `{
			"question":"How should the boundary be designed?",
			"context":"Choose the design and implementation strategy"
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitBriefToolName, `{
			"objective":"Implement an autonomous plan",
			"conditions":["Use a stable abstraction"],
			"decisions":["Use a small interface with an efficient concrete implementation"],
			"acceptance_criteria":["The approved task completes"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitPlanToolName, `{
			"outcome":"succeeded",
			"summary":"Execute an autonomous plan",
			"conditions":["Use a stable abstraction"],
			"steps":[{"title":"Implement boundary","description":"Add the selected implementation","target":{"any":[{"all":[{"kind":"paths","paths":["app/plan.go"]}]}]}}],
			"verification":["Complete the autonomous plan"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.CoderCompleteToolName, `{
			"outcome":"succeeded",
			"summary":"Implemented the selected boundary",
			"artifacts":["app/plan.go"],
			"verification":["go test ./app"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.ReviewTaskToolName, `{
			"decision":"next",
			"feedback":"",
			"facts":["The autonomous plan completed"]
		}`)}},
	}}
	agent, workspaceStore, connection := testACPAgent(t, configuredClient, &fakeAgentTools{})
	agent.state.config.Provider.Model = "plan-model"
	agent.planOverrides = PlanAutomationOverrides{
		AutoApprove: BooleanOverride{Set: true, Value: true},
		AutoResolve: BooleanOverride{Set: true, Value: true},
	}
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("/plan implement an autonomous boundary")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonEndTurn || len(connection.elicitationSnapshot()) != 0 {
		t.Fatalf("autonomous response = %#v, elicitations = %#v", response, connection.elicitationSnapshot())
	}
	var sawAutoResolve bool
	for _, request := range configuredClient.requests {
		for _, message := range request.Messages {
			if message.Role == client.RoleTool && message.Name == subagent.AskToUserToolName &&
				strings.Contains(message.Content, `"source":"auto-resolve"`) &&
				strings.Contains(message.Content, "smallest stable abstraction") {
				sawAutoResolve = true
			}
		}
	}
	if !sawAutoResolve {
		t.Fatalf("Griller requests omitted the auto-resolve answer: %#v", configuredClient.requests)
	}
	activeStore := activeACPWorkspaceStore(t, agent)
	assertArchivedPlanCheckpoint(t, activeStore, 1)
	paths, err := filepath.Glob(filepath.Join(activeStore.ExecutionHistoryDir(), "plan-execution-*.json"))
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
	sources := make(map[string]bool)
	if stored.Checkpoint.Planning != nil {
		for _, event := range stored.Checkpoint.Planning.Events {
			if event.Type == subagent.PlanningEventAnswer {
				sources[event.Agent] = event.Answer != nil && event.Answer.Source == event.Agent
			}
		}
	}
	if !sources[subagent.UserAnswerSourceAutoResolve] || !sources[subagent.UserAnswerSourceAutoApprove] {
		t.Fatalf("automatic answer sources = %#v", sources)
	}
}
