package app

import (
	"context"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
	"github.com/snowmerak/q/workspace"
)

const testDebugReport = `{
	"summary":"The failure occurs while replacing the session file.",
	"findings":["Windows reports access denied at MoveFileExW"],
	"likely_cause":"A short-lived file handle blocks the atomic replacement.",
	"reasoning":["The temporary file is already written before replacement fails"],
	"confidence":"medium",
	"suggested_fixes":["Retry the replacement up to three times with a short backoff"],
	"verification":["Simulate a transient destination lock and observe recovery"],
	"uncertainties":["The lock owner is not directly observed"]
}`

func debugPlanningClient() *planningClient {
	return &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.DelegateScoutToolName, `{
			"objective":"Locate the session replacement boundary"
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.ScoutCompleteToolName, `{
			"outcome":"succeeded",
			"summary":"The failure occurs in the atomic replacement step",
			"findings":[{"path":"workspace/session.go","summary":"MoveFileExW replaces session.json"}]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitBriefToolName, `{
			"objective":"Investigate session replacement failure",
			"conditions":["The error occurs at MoveFileExW"],
			"acceptance_criteria":["Explain a likely cause and safe fix"],
			"repository_evidence":["session.json is replaced atomically"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitDebugReportToolName, testDebugReport)}},
	}}
}

func TestDebugDefaultResolverCombinesRepositorySkillsAndGeneralSolutions(t *testing.T) {
	answer, err := debugDefaultResolver(t.Context(), subagent.UserQuestion{
		Question: "What is the most likely cause?",
		Context:  "The failure occurs while replacing session.json on Windows.",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"What is the most likely cause?",
		"replacing session.json on Windows",
		"current repository as primary",
		"Agent Skills",
		"general diagnostic patterns and common solutions",
		"do not invent it",
		"evidence needed to confirm or refute it",
	} {
		if !strings.Contains(answer.Freeform, expected) {
			t.Fatalf("debug auto-resolve answer omitted %q: %q", expected, answer.Freeform)
		}
	}
	if answer.Source != subagent.UserAnswerSourceAutoResolve || answer.SelectedChoiceID != "" {
		t.Fatalf("debug auto-resolve answer = %#v", answer)
	}
}

func TestDebugCommandReturnsReportWithoutApprovalOrExecution(t *testing.T) {
	configuredClient := debugPlanningClient()
	value := config.Default()
	value.Provider.Model = "plan-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	store := workspace.Store{Root: t.TempDir()}
	m.workspaceStore = &store
	m.toolRuntime = &fakeAgentTools{}
	m.enterChat(value, configuredClient)
	m.resize(100, 36)

	m.input.SetValue("/debug")
	updated, _ := m.submitChat()
	m = updated.(model)
	if !m.debugArmed || m.planArmed || m.waiting || !strings.Contains(m.input.Placeholder, "issue to investigate") {
		t.Fatalf("armed state: debug=%v plan=%v waiting=%v placeholder=%q", m.debugArmed, m.planArmed, m.waiting, m.input.Placeholder)
	}

	m.input.SetValue("Investigate session replacement failure")
	updated, command := m.submitChat()
	m = updated.(model)
	if m.debugArmed || !m.waiting || command == nil {
		t.Fatalf("started state: armed=%v waiting=%v command=%v", m.debugArmed, m.waiting, command != nil)
	}
	for attempts := 0; m.waiting && attempts < 64; attempts++ {
		updated, command = m.Update(nextAgentMessage(t, command))
		m = updated.(model)
	}
	if m.waiting {
		t.Fatal("debug workflow did not finish")
	}
	final := m.messages[len(m.messages)-1].Content
	if !strings.Contains(final, "Likely cause") || !strings.Contains(final, "short-lived file handle") || !strings.Contains(final, "How to verify") {
		t.Fatalf("debug report = %q", final)
	}
	if len(configuredClient.requests) != 4 {
		t.Fatalf("model requests = %d, want Griller, Scout, resumed Griller, and Planner", len(configuredClient.requests))
	}
	for _, request := range configuredClient.requests {
		if hasPlanTool(request.Tools, "write_file") || hasPlanTool(request.Tools, "edit_file") ||
			hasPlanTool(request.Tools, "run_command") || hasPlanTool(request.Tools, subagent.ReviewTaskToolName) {
			t.Fatalf("debug exposed execution tools: %#v", request.Tools)
		}
		for _, message := range request.Messages {
			if message.Role == client.RoleSystem && strings.Contains(message.Content, "q's Coder") {
				t.Fatalf("debug invoked Coder: %q", message.Content)
			}
		}
	}
}

func TestACPDebugCommandReturnsDiagnosticReport(t *testing.T) {
	configuredClient := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.AskToUserToolName, `{
			"question":"Which platform reproduces the failure?"
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitBriefToolName, `{
			"objective":"Investigate session replacement failure",
			"conditions":["The failure reproduces on Windows"],
			"acceptance_criteria":["Explain a likely cause and safe fix"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitDebugReportToolName, testDebugReport)}},
	}}
	agent, workspaceStore, connection := testACPAgent(t, configuredClient, &fakeAgentTools{})
	agent.state.config.Provider.Model = "plan-model"
	connection.answer = "Windows"
	if _, err := agent.Initialize(t.Context(), acp.InitializeRequest{
		ClientCapabilities: acp.ClientCapabilities{
			Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("/debug investigate session replacement failure")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonEndTurn || len(configuredClient.requests) != 3 {
		t.Fatalf("response=%#v requests=%d", response, len(configuredClient.requests))
	}
	if len(connection.elicitationSnapshot()) != 1 {
		t.Fatalf("debug elicitations = %#v", connection.elicitationSnapshot())
	}
	var receivedAnswer string
	for _, message := range configuredClient.requests[1].Messages {
		if message.Role == client.RoleTool && message.Name == subagent.AskToUserToolName {
			receivedAnswer = message.Content
		}
	}
	if !strings.Contains(receivedAnswer, `"freeform":"Windows"`) {
		t.Fatalf("debug answer = %q", receivedAnswer)
	}
	var output, thought string
	var statuses []acp.PlanEntryStatus
	for _, notification := range connection.snapshot() {
		if update := notification.Update.AgentMessageChunk; update != nil && update.Content.Text != nil {
			output += update.Content.Text.Text
		}
		if update := notification.Update.AgentThoughtChunk; update != nil && update.Content.Text != nil {
			thought += update.Content.Text.Text
		}
		if update := notification.Update.Plan; update != nil && len(update.Entries) == 1 && update.Entries[0].Content == "investigate session replacement failure" {
			statuses = append(statuses, update.Entries[0].Status)
		}
	}
	if !strings.Contains(output, "Likely cause") || !strings.Contains(output, "Suggested fix") {
		t.Fatalf("ACP debug output = %q", output)
	}
	if !strings.Contains(thought, "griller") || !strings.Contains(thought, "planner") || strings.Contains(thought, "executor") {
		t.Fatalf("ACP debug progress = %q", thought)
	}
	if len(statuses) != 2 || statuses[0] != acp.PlanEntryStatusInProgress || statuses[1] != acp.PlanEntryStatusCompleted {
		t.Fatalf("ACP debug lifecycle = %#v", statuses)
	}
}

func TestACPDebugAutoResolveUsesDiagnosticResolverWithoutElicitation(t *testing.T) {
	configuredClient := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.AskToUserToolName, `{
			"question":"What is the most likely cause?",
			"context":"Choose a diagnosis and fix direction"
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitBriefToolName, `{
			"objective":"Investigate session replacement failure",
			"conditions":["Use repository evidence, skills, and established diagnostic patterns"],
			"acceptance_criteria":["Explain a likely cause and safe fix"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitDebugReportToolName, testDebugReport)}},
	}}
	agent, workspaceStore, connection := testACPAgent(t, configuredClient, &fakeAgentTools{})
	agent.state.config.Provider.Model = "plan-model"
	agent.state.config.Plan.AutoResolve = true
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("/debug investigate session replacement failure")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonEndTurn || len(configuredClient.requests) != 3 {
		t.Fatalf("response=%#v requests=%d", response, len(configuredClient.requests))
	}
	if len(connection.elicitationSnapshot()) != 0 {
		t.Fatalf("auto-resolved debug elicitations = %#v", connection.elicitationSnapshot())
	}
	var receivedAnswer string
	for _, message := range configuredClient.requests[1].Messages {
		if message.Role == client.RoleTool && message.Name == subagent.AskToUserToolName {
			receivedAnswer = message.Content
		}
	}
	for _, expected := range []string{
		`"source":"auto-resolve"`,
		"current repository as primary",
		"Agent Skills",
		"general diagnostic patterns and common solutions",
	} {
		if !strings.Contains(receivedAnswer, expected) {
			t.Fatalf("debug auto-resolve tool answer omitted %q: %q", expected, receivedAnswer)
		}
	}
}
