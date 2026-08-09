package commitagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/subagent"
)

func TestPrepareRepositoryAutoStagesAndHidesLocksFromOverview(t *testing.T) {
	root := newTestRepository(t)
	writeTestFile(t, root, "AGENTS.md", "Use conventional commits.\n")
	writeTestFile(t, root, "app/main.go", "package app\n")
	writeTestFile(t, root, "go.sum", "module checksum\n")

	state, err := prepareRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !state.autoStaged {
		t.Fatal("changes were not auto-staged")
	}
	if !reflect.DeepEqual(state.visibleFiles, []string{"AGENTS.md", "app/main.go"}) {
		t.Fatalf("visible files = %#v", state.visibleFiles)
	}
	if !reflect.DeepEqual(state.lockFiles, []string{"go.sum"}) {
		t.Fatalf("lock files = %#v", state.lockFiles)
	}
	if len(state.contextFiles) != 1 || state.contextFiles[0].Path != "AGENTS.md" {
		t.Fatalf("context files = %#v", state.contextFiles)
	}
	if state.fullDiff == "" || state.fileDiffs["go.sum"] == "" {
		t.Fatal("staged diffs were not retained")
	}

	runtime := commitToolRuntime{state: state}
	result := runtime.call(context.Background(), toolCall(toolGitOverview, `{}`))
	if result.IsError || strings.Contains(result.Content, "go.sum") {
		t.Fatalf("overview result = %#v", result)
	}
}

func TestDetectTrivialWhitespaceAndImportChanges(t *testing.T) {
	t.Run("whitespace", func(t *testing.T) {
		root := newTestRepository(t)
		writeTestFile(t, root, "main.go", "package main\n\nvar answer = 42\n")
		gitTest(t, root, "add", "-A")
		gitTest(t, root, "commit", "-m", "chore: initial")
		writeTestFile(t, root, "main.go", "package main\n\nvar answer  = 42\n")
		state, err := prepareRepository(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		trivial, err := detectTrivialChange(context.Background(), state)
		if err != nil || !trivial.Found || trivial.Message != "style: formatted code" {
			t.Fatalf("trivial = %#v, err = %v", trivial, err)
		}
	})

	t.Run("imports", func(t *testing.T) {
		diff := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1,2 +1,2 @@\n-import z\n import a\n+import z\n"
		if !importsOnly(diff) {
			t.Fatal("reordered imports were not detected")
		}
		if importsOnly("@@ -1 +1 @@\n-old value\n+new value\n") {
			t.Fatal("ordinary edit was classified as imports")
		}
	})
}

func TestCommitToolsRequireOverviewAndValidateProposal(t *testing.T) {
	state := repositoryState{
		visibleFiles: []string{"app/main.go"}, files: []string{"app/main.go"},
		fileDiffs: map[string]string{"app/main.go": "@@ -1 +1 @@\n-old\n+new\n"},
	}
	runtime := commitToolRuntime{state: state}
	result := runtime.call(context.Background(), toolCall(toolProposeCommit, `{"type":"feat","scope":"app","summary":"updated behavior"}`))
	if !result.IsError || !strings.Contains(result.Content, "git_overview") {
		t.Fatalf("premature proposal result = %#v", result)
	}
	if result := runtime.call(context.Background(), toolCall(toolGitOverview, `{}`)); result.IsError {
		t.Fatal(result.Content)
	}
	result = runtime.call(context.Background(), toolCall(toolProposeCommit, `{"type":"feat","scope":"App Space","summary":"updated behavior"}`))
	if !result.IsError || runtime.proposal.Single != nil {
		t.Fatalf("invalid proposal result = %#v, proposal = %#v", result, runtime.proposal)
	}
	result = runtime.call(context.Background(), toolCall(toolProposeCommit, `{"type":"feat","scope":"app","summary":"updated behavior","body":["Covered the new path."]}`))
	if result.IsError || runtime.proposal.Single == nil || formatSubject(*runtime.proposal.Single) != "feat(app): updated behavior" {
		t.Fatalf("valid proposal result = %#v, proposal = %#v", result, runtime.proposal)
	}
}

func TestCommitToolsAutomaticallyCaptureResultsInLoom(t *testing.T) {
	root := t.TempDir()
	runtime := commitToolRuntime{state: repositoryState{
		root: root, visibleFiles: []string{"app/main.go"}, files: []string{"app/main.go"},
		fileDiffs: map[string]string{"app/main.go": strings.Repeat("+changed line\n", 4096)},
	}}
	if err := runtime.enableLoom(root); err != nil {
		t.Fatal(err)
	}
	if result := runtime.call(context.Background(), toolCall(toolGitOverview, `{}`)); result.IsError {
		t.Fatal(result.Content)
	}
	result := runtime.call(context.Background(), toolCall(toolGitFileDiff, `{"path":"app/main.go"}`))
	if result.IsError {
		t.Fatal(result.Content)
	}
	var receipt struct {
		LoomRef string `json:"loom_ref"`
		Preview string `json:"preview"`
		Stored  bool   `json:"stored"`
	}
	if err := json.Unmarshal([]byte(result.Content), &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.Stored || receipt.LoomRef == "" || receipt.Preview == "" {
		t.Fatalf("Loom receipt = %#v", receipt)
	}
	read := runtime.call(context.Background(), toolCall(toolLoomRead,
		fmt.Sprintf(`{"ref":%q,"limit":4096}`, receipt.LoomRef)))
	var readOutput struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(read.Content), &readOutput); err != nil {
		t.Fatal(err)
	}
	if read.IsError || !strings.Contains(readOutput.Content, `"server":"commit-agent"`) {
		t.Fatalf("Loom read result = %#v", read)
	}
}

func TestCommitAgentForcesOverviewAndFallsBackAfterThreeReminders(t *testing.T) {
	responses := []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{toolCall(toolGitOverview, `{}`)}},
		{Role: client.RoleAssistant, Content: "feat: ignored plain response"},
		{Role: client.RoleAssistant, Content: "feat: ignored again"},
		{Role: client.RoleAssistant, Content: "feat: still ignored"},
		{Role: client.RoleAssistant, Content: "feat: final ignored response"},
	}
	fake := &fakeAgentClient{responses: responses}
	state := repositoryState{visibleFiles: []string{"app/main.go"}, scopeCandidates: []string{"app"}, fileDiffs: map[string]string{"app/main.go": "diff"}}
	var progress bytes.Buffer
	proposal, fallback, err := runCommitAgent(context.Background(), fake, subagent.Spec{Model: "commit-model"}, state, 2, newProgressLogger(&progress))
	if err != nil {
		t.Fatal(err)
	}
	if !fallback || proposal.Single == nil || formatSubject(*proposal.Single) != "chore(app): updated staged changes" {
		t.Fatalf("proposal = %#v, fallback = %v", proposal, fallback)
	}
	if len(fake.requests) != 5 {
		t.Fatalf("requests = %d", len(fake.requests))
	}
	choice, ok := fake.requests[0].ToolChoice.(map[string]any)
	if !ok || choice["type"] != "function" {
		t.Fatalf("first tool choice = %#v", fake.requests[0].ToolChoice)
	}
	if len(fake.requests[0].Tools) != 10 {
		t.Fatalf("commit tools = %d", len(fake.requests[0].Tools))
	}
	for _, expected := range []string{"calling git_overview", "sending proposal reminder 3/3", "proposal reminders exhausted"} {
		if !strings.Contains(progress.String(), expected) {
			t.Fatalf("progress log does not contain %q:\n%s", expected, progress.String())
		}
	}
}

func TestCommitAgentAcceptsToolProposal(t *testing.T) {
	fake := &fakeAgentClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{toolCall(toolGitOverview, `{}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{toolCall(toolProposeCommit, `{"type":"feat","scope":"commit","summary":"added commit generation","body":["Generated validated messages."]}`)}},
	}}
	state := repositoryState{visibleFiles: []string{"commitagent/agent.go"}, fileDiffs: map[string]string{"commitagent/agent.go": "diff"}}
	var progress bytes.Buffer
	proposal, fallback, err := runCommitAgent(context.Background(), fake, subagent.Spec{Model: "commit-model", ReasoningEffort: "high"}, state, 2, newProgressLogger(&progress))
	if err != nil || fallback || proposal.Single == nil {
		t.Fatalf("proposal = %#v, fallback = %v, err = %v", proposal, fallback, err)
	}
	if got := formatCommitMessage(*proposal.Single); got != "feat(commit): added commit generation\n\n- Generated validated messages." {
		t.Fatalf("message = %q", got)
	}
	if fake.requests[0].Model != "commit-model" || fake.requests[0].ReasoningEffort != "high" {
		t.Fatalf("request model controls = %#v", fake.requests[0])
	}
	if !strings.Contains(progress.String(), "accepted feat(commit): added commit generation") {
		t.Fatalf("progress log = %q", progress.String())
	}
}

func TestExecuteSplitCommitsPreservesFileGroups(t *testing.T) {
	root := newTestRepository(t)
	writeTestFile(t, root, "app.txt", "old app\n")
	writeTestFile(t, root, "docs.txt", "old docs\n")
	gitTest(t, root, "add", "-A")
	gitTest(t, root, "commit", "-m", "chore: initial")
	writeTestFile(t, root, "app.txt", "new app\n")
	writeTestFile(t, root, "docs.txt", "new docs\n")
	gitTest(t, root, "add", "-A")
	state, err := prepareRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	proposals := []Proposal{
		{Type: "feat", Scope: "app", Summary: "updated application", Files: []string{"app.txt"}},
		{Type: "docs", Summary: "updated documentation", Files: []string{"docs.txt"}},
	}
	var output bytes.Buffer
	result, err := executeProposal(context.Background(), state, proposalState{Split: proposals}, "commit agent", newProgressLogger(&output))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Split || len(result.Messages) != 2 {
		t.Fatalf("result = %#v", result)
	}
	subjects := strings.Fields(strings.TrimSpace(gitTest(t, root, "log", "-2", "--format=%s")))
	log := gitTest(t, root, "log", "-2", "--format=%s")
	if !strings.Contains(log, "docs: updated documentation") || !strings.Contains(log, "feat(app): updated application") || len(subjects) < 4 {
		t.Fatalf("log = %q", log)
	}
	if staged := strings.TrimSpace(gitTest(t, root, "diff", "--cached", "--name-only")); staged != "" {
		t.Fatalf("staged files remain: %q", staged)
	}
	if got := gitTest(t, root, "show", "HEAD^:docs.txt"); got != "old docs\n" {
		t.Fatalf("first commit included docs change: %q", got)
	}
}

func TestSessionEditsSelectedSplitMessageWithoutChangingFiles(t *testing.T) {
	session := &Session{proposal: proposalState{Split: []Proposal{
		{Type: "feat", Scope: "app", Summary: "added application flow", Files: []string{"app.go"}},
		{Type: "docs", Summary: "documented application flow", Files: []string{"README.md"}},
	}}}
	model := newCommitUIModel(context.Background(), t.TempDir())
	defer model.cancel()
	model.session = session
	model.proposals = session.Proposals()
	model.phase = commitUIReview
	model.selected = 1
	view := model.View().Content
	for _, expected := range []string{"Split proposal", "README.md", "e edit message", "p commit+push"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("review view does not contain %q:\n%s", expected, view)
		}
	}

	updated, command := model.updateReview(tea.KeyPressMsg{Code: 'e', Text: "e"})
	model = updated.(commitUIModel)
	if model.phase != commitUIEditing {
		t.Fatalf("edit state = %v, command nil = %v", model.phase, command == nil)
	}
	model.editor.SetValue("docs(commit): documented interactive review\n\n- Explained message editing.")
	updated, _ = model.updateEditor(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	model = updated.(commitUIModel)
	proposals := session.Proposals()
	if model.phase != commitUIReview || proposals[1].Scope != "commit" || proposals[1].Summary != "documented interactive review" {
		t.Fatalf("updated model = phase %v, proposal %#v", model.phase, proposals[1])
	}
	if !reflect.DeepEqual(proposals[1].Files, []string{"README.md"}) || !reflect.DeepEqual(proposals[0].Files, []string{"app.go"}) {
		t.Fatalf("split files changed: %#v", proposals)
	}
}

func TestSessionRejectsChangedIndexBeforeCommit(t *testing.T) {
	root := newTestRepository(t)
	writeTestFile(t, root, "app.txt", "old\n")
	gitTest(t, root, "add", "-A")
	gitTest(t, root, "commit", "-m", "chore: initial")
	writeTestFile(t, root, "app.txt", "first\n")
	gitTest(t, root, "add", "-A")
	state, err := prepareRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	proposal := Proposal{Type: "feat", Scope: "app", Summary: "updated application"}
	session := &Session{state: state, proposal: proposalState{Single: &proposal}, source: "edited proposal"}
	writeTestFile(t, root, "app.txt", "second\n")
	gitTest(t, root, "add", "-A")
	if _, err := session.Commit(context.Background(), newProgressLogger(nil)); err == nil || !strings.Contains(err.Error(), "changed during review") {
		t.Fatalf("commit error = %v", err)
	}
}

func TestSessionCommitThenPushRequiresUpstream(t *testing.T) {
	root := newTestRepository(t)
	writeTestFile(t, root, "app.txt", "content\n")
	state, err := prepareRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	proposal := Proposal{Type: "feat", Scope: "app", Summary: "added application"}
	session := &Session{state: state, proposal: proposalState{Single: &proposal}, source: "commit agent"}
	result, err := session.Commit(context.Background(), newProgressLogger(nil))
	if err != nil || len(result.Messages) != 1 {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	if err := session.Push(context.Background(), newProgressLogger(nil)); err == nil || !strings.Contains(err.Error(), "no upstream") {
		t.Fatalf("push error = %v", err)
	}
}

func TestCommitUIApprovesProposalBeforeCreatingCommit(t *testing.T) {
	root := newTestRepository(t)
	writeTestFile(t, root, "app.txt", "content\n")
	state, err := prepareRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	proposal := Proposal{Type: "feat", Scope: "app", Summary: "added application"}
	session := &Session{state: state, proposal: proposalState{Single: &proposal}, source: "commit agent"}
	model := newCommitUIModel(context.Background(), root)
	defer model.cancel()
	model.session = session
	model.proposals = session.Proposals()
	model.phase = commitUIReview

	updated, command := model.updateReview(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(commitUIModel)
	if model.phase != commitUIWorking || command == nil {
		t.Fatalf("working state = %v, command nil = %v", model.phase, command == nil)
	}
	updated, _ = model.Update(command())
	model = updated.(commitUIModel)
	if model.phase != commitUIDone || len(model.result.Messages) != 1 || model.status != "Commit created" {
		t.Fatalf("completed model = phase %v, result %#v, status %q", model.phase, model.result, model.status)
	}
	if got := strings.TrimSpace(gitTest(t, root, "log", "-1", "--format=%s")); got != "feat(app): added application" {
		t.Fatalf("commit subject = %q", got)
	}
}

func TestSessionPushesCommittedProposalToExistingUpstream(t *testing.T) {
	root := newTestRepository(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, remote, "init", "--bare", "-q")
	writeTestFile(t, root, "app.txt", "initial\n")
	gitTest(t, root, "add", "-A")
	gitTest(t, root, "commit", "-m", "chore: initial")
	gitTest(t, root, "remote", "add", "origin", remote)
	gitTest(t, root, "push", "-u", "origin", "HEAD")

	writeTestFile(t, root, "app.txt", "updated\n")
	state, err := prepareRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	proposal := Proposal{Type: "feat", Scope: "app", Summary: "updated application"}
	session := &Session{state: state, proposal: proposalState{Single: &proposal}, source: "commit agent"}
	if _, err := session.Commit(context.Background(), newProgressLogger(nil)); err != nil {
		t.Fatal(err)
	}
	if err := session.Push(context.Background(), newProgressLogger(nil)); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(gitTest(t, remote, "log", "-1", "--format=%s")); got != "feat(app): updated application" {
		t.Fatalf("remote subject = %q", got)
	}
}

type fakeAgentClient struct {
	mu        sync.Mutex
	responses []client.Message
	requests  []client.ChatRequest
}

func (fake *fakeAgentClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.requests = append(fake.requests, request)
	if len(fake.responses) == 0 {
		return nil, os.ErrNotExist
	}
	message := fake.responses[0]
	fake.responses = fake.responses[1:]
	return &client.ChatResponse{Choices: []client.Choice{{Message: message}}}, nil
}

func toolCall(name, arguments string) client.ToolCall {
	return client.ToolCall{ID: "call-" + name, Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: name, Arguments: arguments}}
}

func newTestRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitTest(t, root, "init", "-q")
	gitTest(t, root, "config", "user.name", "Q Test")
	gitTest(t, root, "config", "user.email", "q-test@example.com")
	return root
}

func writeTestFile(t *testing.T, root, path, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitTest(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	body, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, body)
	}
	return string(body)
}
