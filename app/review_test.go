package app

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
	"github.com/snowmerak/q/workspace"
)

const testReviewReport = `{
	"summary":"The supplied change has no actionable correctness issue.",
	"verdict":"approve",
	"findings":[],
	"verification_gaps":["Tests were not run by the read-only review workflow"]
}`

func initializeChangedReviewRepository(t *testing.T, root string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable")
	}
	if output, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", output, err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readSingleReviewExecution(t *testing.T, store workspace.Store) subagent.ReviewExecutionLog {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(store.ReviewExecutionHistoryDir(), "review-execution-*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("review execution logs=%v, err=%v", paths, err)
	}
	body, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Execution subagent.ReviewExecutionLog `json:"execution"`
	}
	if err := json.Unmarshal(body, &stored); err != nil {
		t.Fatal(err)
	}
	return stored.Execution
}

func TestCollectReviewRequestCapturesBoundedWorkingTreeEvidence(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable")
	}
	root := t.TempDir()
	if output, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", output, err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nvar value = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".q"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".q", "session.json"), []byte("metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	request, err := collectReviewRequest(t.Context(), root, "  focus on regressions  ", "run-review")
	if err != nil {
		t.Fatal(err)
	}
	if request.ID != "run-review" || request.Objective != "focus on regressions" || len(request.Files) != 1 {
		t.Fatalf("request = %#v", request)
	}
	file := request.Files[0]
	if file.Path != "main.go" || file.Status != "??" || len(file.Sections) != 1 ||
		!strings.Contains(file.Sections[0].Patch, "+var value = 1") {
		t.Fatalf("reviewed file = %#v", file)
	}
}

func TestCollectReviewRequestDefaultsRequestAndRejectsCleanTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable")
	}
	root := t.TempDir()
	if output, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", output, err)
	}
	if _, err := collectReviewRequest(t.Context(), root, "", ""); err == nil || !strings.Contains(err.Error(), "no changes") {
		t.Fatalf("clean review error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	request, err := collectReviewRequest(t.Context(), root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if request.Objective != defaultReviewRequest {
		t.Fatalf("default request = %q", request.Objective)
	}
}

func TestCollectReviewRequestBoundsAggregatePatchInput(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable")
	}
	root := t.TempDir()
	if output, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", output, err)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(strings.Repeat(name, 100<<10)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	request, err := collectReviewRequest(t.Context(), root, "review large changes", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Files) != 3 || len(request.Files[2].Sections) != 1 {
		t.Fatalf("files = %#v", request.Files)
	}
	last := request.Files[2].Sections[0]
	if !last.Truncated || !strings.Contains(last.Patch, "aggregate review input reached its safety limit") {
		t.Fatalf("last patch was not bounded: %#v", last)
	}
}

func TestReviewIsExposedAsALocalSlashCommand(t *testing.T) {
	for _, command := range localSlashCommands {
		if command.name == "/review" {
			if command.arguments != "[request]" {
				t.Fatalf("review arguments = %q", command.arguments)
			}
			return
		}
	}
	t.Fatal("review command is missing")
}

func TestTUIReviewCommandReturnsAndArchivesReport(t *testing.T) {
	root := t.TempDir()
	initializeChangedReviewRepository(t, root)
	configuredClient := &planningClient{responses: []client.Message{{
		Role:      client.RoleAssistant,
		ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitReviewReportToolName, testReviewReport)},
	}}}
	value := config.Default()
	value.Provider.Model = "plan-model"
	store := workspace.Store{Root: root}
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &store
	m.toolRuntime = &fakeAgentTools{}
	m.enterChat(value, configuredClient)
	m.resize(100, 36)

	m.input.SetValue("/review focus on regressions")
	updated, command := m.submitChat()
	m = updated.(model)
	if !m.waiting || command == nil {
		t.Fatalf("review did not start: waiting=%v command=%v status=%q", m.waiting, command != nil, m.status)
	}
	for attempts := 0; m.waiting && attempts < 64; attempts++ {
		updated, command = m.Update(nextAgentMessage(t, command))
		m = updated.(model)
	}
	if m.waiting {
		t.Fatal("review workflow did not finish")
	}
	final := m.messages[len(m.messages)-1].Content
	if !strings.Contains(final, "No actionable findings") || !strings.Contains(final, "Verdict: approve") {
		t.Fatalf("review report = %q", final)
	}
	if len(configuredClient.requests) != 1 || !strings.Contains(configuredClient.requests[0].Messages[1].Content, "focus on regressions") {
		t.Fatalf("review requests = %#v", configuredClient.requests)
	}
	execution := readSingleReviewExecution(t, store)
	if execution.Outcome != subagent.ReviewOutcomeSucceeded || execution.Report == nil || execution.Report.Verdict != "approve" ||
		execution.Objective != "focus on regressions" {
		t.Fatalf("archived review execution = %#v", execution)
	}
}

func TestACPReviewCommandUsesDefaultRequestAndArchivesReport(t *testing.T) {
	configuredClient := &planningClient{responses: []client.Message{{
		Role:      client.RoleAssistant,
		ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitReviewReportToolName, testReviewReport)},
	}}}
	agent, workspaceStore, connection := testACPAgent(t, configuredClient, &fakeAgentTools{})
	initializeChangedReviewRepository(t, workspaceStore.Root)
	agent.state.config.Provider.Model = "plan-model"
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("/review")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonEndTurn || len(configuredClient.requests) != 1 {
		t.Fatalf("response=%#v requests=%d", response, len(configuredClient.requests))
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
		if update := notification.Update.Plan; update != nil && len(update.Entries) == 1 && update.Entries[0].Content == defaultReviewRequest {
			statuses = append(statuses, update.Entries[0].Status)
		}
	}
	if !strings.Contains(output, "Verdict: approve") || !strings.Contains(thought, "review") {
		t.Fatalf("ACP review output=%q thought=%q", output, thought)
	}
	if len(statuses) != 2 || statuses[0] != acp.PlanEntryStatusInProgress || statuses[1] != acp.PlanEntryStatusCompleted {
		t.Fatalf("ACP review lifecycle = %#v", statuses)
	}
	execution := readSingleReviewExecution(t, activeACPWorkspaceStore(t, agent))
	if execution.Outcome != subagent.ReviewOutcomeSucceeded || execution.Report == nil || execution.Objective != defaultReviewRequest {
		t.Fatalf("archived ACP review execution = %#v", execution)
	}
}
