package app

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
	"github.com/snowmerak/q/workspace"
)

func TestACPParticipantKeepsSessionAndRestoresOnlyCompletedExchanges(t *testing.T) {
	store, lock, err := workspace.CreateSession(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(workspace.Session{}); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Error(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	connections := map[string]config.AgentConnectionConfig{"research": {Command: executable}}
	starts := 0
	var prompts []string
	makeRunner := func() *ParticipantRunner {
		runner := NewParticipantRunner(t.Context(), nil, connections)
		runner.start = func(_ context.Context, _ acpAgentCommand, root, _, _ string, _ io.Writer) (*acpRemoteClient, error) {
			starts++
			if root != store.SessionDir() {
				t.Fatalf("independent ACP cwd = %s", root)
			}
			connection := &fakeACPRemoteConnection{}
			remote := &acpRemoteClient{connection: connection, sessionID: "test-session", root: root}
			connection.prompt = func(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
				text := request.Prompt[0].Text.Text
				prompts = append(prompts, text)
				kind := acp.ToolKindEdit
				permission, err := remote.RequestPermission(ctx, acp.RequestPermissionRequest{
					SessionId: request.SessionId, ToolCall: acp.ToolCallUpdate{ToolCallId: "edit", Kind: &kind},
					Options: []acp.PermissionOption{{OptionId: "yes", Kind: acp.PermissionOptionKindAllowOnce}},
				})
				if err != nil || permission.Outcome.Cancelled == nil {
					t.Fatalf("ACP edit was not rejected: %+v, %v", permission, err)
				}
				if text == "fail" {
					return acp.PromptResponse{}, errors.New("temporary failure")
				}
				err = remote.SessionUpdate(ctx, acp.SessionNotification{SessionId: request.SessionId, Update: acp.SessionUpdate{
					AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{Content: acp.TextBlock("saved answer")},
				}})
				return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, err
			}
			return remote, nil
		}
		return runner
	}
	runner := makeRunner()
	run := func(operation, prompt string) error {
		return runner.RunWithOptions(t.Context(), store, store.SessionID, prompt, SessionOptions{Agent: "research", SystemPrompt: "Council instructions", OperationID: operation}, func(event SessionEvent) error {
			if event.Content != "saved answer" || event.SessionID != store.SessionID {
				t.Fatalf("result = %+v", event)
			}
			return nil
		})
	}
	if err := run("round-1", "first opinion"); err != nil {
		t.Fatal(err)
	}
	if err := run("round-2", "peer review"); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || prompts[1] != "peer review" || !strings.Contains(prompts[0], "Council instructions") {
		t.Fatalf("session not retained: starts=%d prompts=%v", starts, prompts)
	}
	if err := run("round-3", "fail"); err == nil {
		t.Fatal("expected failed prompt")
	}
	if err := runner.ReleaseSession(store, store.SessionID); err != nil {
		t.Fatal(err)
	}
	runner = makeRunner()
	if err := run("round-2", "peer review"); err != nil || starts != 1 || len(prompts) != 3 {
		t.Fatalf("saved result was not reused: starts=%d prompts=%v err=%v", starts, prompts, err)
	}
	if err := run("round-2", "changed prompt"); err == nil {
		t.Fatal("accepted changed operation prompt")
	}
	if err := run("round-3", "retry review"); err != nil {
		t.Fatal(err)
	}
	last := prompts[len(prompts)-1]
	if starts != 2 || !strings.Contains(last, "first opinion") || !strings.Contains(last, "peer review") || !strings.Contains(last, "saved answer") || strings.Contains(last, "\nfail\n") {
		t.Fatalf("incorrect restored context: starts=%d prompt=%s", starts, last)
	}
	if err := runner.ReleaseSession(store, store.SessionID); err != nil {
		t.Error(err)
	}
}

func TestACPParticipantRejectsUnavailableConnectionsAndModelControls(t *testing.T) {
	runner := NewParticipantRunner(t.Context(), nil, map[string]config.AgentConnectionConfig{"disabled": {Disabled: true}})
	for _, options := range []SessionOptions{
		{Agent: "missing", OperationID: "round-1"},
		{Agent: "disabled", OperationID: "round-1"},
		{Agent: "missing", Model: "model", OperationID: "round-1"},
		{Agent: "missing", ReasoningEffort: "high", OperationID: "round-1"},
		{Agent: "missing"},
	} {
		if err := runner.RunWithOptions(t.Context(), workspace.Store{}, "", "question", options, nil); err == nil {
			t.Fatalf("accepted invalid options: %+v", options)
		}
	}
}

func TestACPParticipantRejectsEmptyAndIncompleteResponses(t *testing.T) {
	for _, test := range []struct {
		text string
		stop acp.StopReason
	}{{"", acp.StopReasonEndTurn}, {"partial", acp.StopReasonMaxTokens}, {"cancelled", acp.StopReasonCancelled}} {
		t.Run(string(test.stop)+test.text, func(t *testing.T) {
			connection := &fakeACPRemoteConnection{}
			remote := &acpRemoteClient{connection: connection, sessionID: "test", requireCompletedText: true}
			connection.prompt = func(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
				if err := remote.SessionUpdate(ctx, acp.SessionNotification{SessionId: request.SessionId, Update: acp.SessionUpdate{AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{Content: acp.TextBlock(test.text)}}}); err != nil {
					t.Error(err)
				}
				return acp.PromptResponse{StopReason: test.stop}, nil
			}
			if _, _, err := remote.prompt(t.Context(), "question", nil); err == nil {
				t.Fatal("accepted empty or incomplete answer")
			}
		})
	}
}
