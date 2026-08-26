package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk"
	"github.com/snowmerak/q/client"
)

type fakeACPRemoteConnection struct {
	newSessionID acp.SessionId
	newErr       error
	closed       []acp.SessionId
	deleted      []acp.SessionId
	closeErr     error
	deleteErr    error
	prompt       func(context.Context, acp.PromptRequest) (acp.PromptResponse, error)
}

func (f *fakeACPRemoteConnection) NewSession(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	return acp.NewSessionResponse{SessionId: f.newSessionID}, f.newErr
}

func (*fakeACPRemoteConnection) LoadSession(context.Context, acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	return acp.LoadSessionResponse{}, nil
}

func (*fakeACPRemoteConnection) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, nil
}

func (f *fakeACPRemoteConnection) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	if f.prompt == nil {
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}
	return f.prompt(ctx, request)
}

func (f *fakeACPRemoteConnection) CloseSession(_ context.Context, request acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	f.closed = append(f.closed, request.SessionId)
	return acp.CloseSessionResponse{}, f.closeErr
}

func (f *fakeACPRemoteConnection) UnstableDeleteSession(_ context.Context, request acp.UnstableDeleteSessionRequest) (acp.UnstableDeleteSessionResponse, error) {
	f.deleted = append(f.deleted, request.SessionId)
	return acp.UnstableDeleteSessionResponse{}, f.deleteErr
}

func TestResolveACPAgentCommand(t *testing.T) {
	paths := map[string]string{"codex-acp": "C:/bin/codex-acp.exe", "npx": "C:/bin/npx.cmd", "grok": "C:/bin/grok.exe"}
	lookPath := func(name string) (string, error) {
		path, ok := paths[name]
		if !ok {
			return "", errors.New("missing")
		}
		return path, nil
	}
	codex, err := resolveACPAgentCommand("codex", lookPath)
	if err != nil || codex.name != paths["codex-acp"] || len(codex.args) != 0 {
		t.Fatalf("codex command = %#v, %v", codex, err)
	}
	delete(paths, "codex-acp")
	codex, err = resolveACPAgentCommand("codex", lookPath)
	if err != nil || codex.name != paths["npx"] || !slices.Equal(codex.args, []string{"-y", "@agentclientprotocol/codex-acp"}) {
		t.Fatalf("codex npx command = %#v, %v", codex, err)
	}
	grok, err := resolveACPAgentCommand("grok", lookPath)
	if err != nil || grok.name != paths["grok"] || !slices.Equal(grok.args, []string{"agent", "stdio"}) {
		t.Fatalf("grok command = %#v, %v", grok, err)
	}
	if _, err := resolveACPAgentCommand("unknown", lookPath); err == nil {
		t.Fatal("unknown ACP agent was accepted")
	}
}

func TestACPRemoteClientStreamsToolsAndPermissions(t *testing.T) {
	connection := &fakeACPRemoteConnection{}
	remote := &acpRemoteClient{
		connection: connection, sessionID: "session-1", display: "Test Agent", agent: "test",
	}
	connection.prompt = func(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
		if request.SessionId != "session-1" || len(request.Prompt) != 1 || request.Prompt[0].Text == nil ||
			request.Prompt[0].Text.Text != "hello" {
			t.Fatalf("prompt request = %#v", request)
		}
		if err := remote.SessionUpdate(ctx, acp.SessionNotification{SessionId: "session-1", Update: acp.SessionUpdate{
			AgentThoughtChunk: &acp.SessionUpdateAgentThoughtChunk{Content: acp.TextBlock("thinking ")},
		}}); err != nil {
			return acp.PromptResponse{}, err
		}
		if err := remote.SessionUpdate(ctx, acp.SessionNotification{SessionId: "session-1", Update: acp.SessionUpdate{
			ToolCall: &acp.SessionUpdateToolCall{
				ToolCallId: "tool-1", Title: "Edit file", Status: acp.ToolCallStatusPending,
			},
		}}); err != nil {
			return acp.PromptResponse{}, err
		}
		title := "Edit file"
		permission, err := remote.RequestPermission(ctx, acp.RequestPermissionRequest{
			SessionId: "session-1",
			ToolCall:  acp.ToolCallUpdate{ToolCallId: "tool-1", Title: &title, RawInput: map[string]any{"path": "main.go"}},
			Options: []acp.PermissionOption{
				{OptionId: "allow", Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce},
				{OptionId: "reject", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
			},
		})
		if err != nil || permission.Outcome.Selected == nil || permission.Outcome.Selected.OptionId != "allow" {
			t.Fatalf("permission = %#v, %v", permission, err)
		}
		completed := acp.ToolCallStatusCompleted
		if err := remote.SessionUpdate(ctx, acp.SessionNotification{SessionId: "session-1", Update: acp.SessionUpdate{
			ToolCallUpdate: &acp.SessionToolCallUpdate{ToolCallId: "tool-1", Status: &completed},
		}}); err != nil {
			return acp.PromptResponse{}, err
		}
		if err := remote.SessionUpdate(ctx, acp.SessionNotification{SessionId: "session-1", Update: acp.SessionUpdate{
			AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{Content: acp.TextBlock("done")},
		}}); err != nil {
			return acp.PromptResponse{}, err
		}
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}

	events := make(chan agentEvent)
	go remote.runPrompt(t.Context(), "hello", events)
	var thought, response strings.Builder
	var actions []string
	var final *client.ChatResponse
	for event := range events {
		if event.question != nil {
			if !event.question.ChoiceOnly || len(event.question.Choices) != 2 {
				t.Fatalf("permission question = %#v", event.question)
			}
			event.answer <- askToUserOutput{SelectedChoiceID: "allow"}
		}
		if event.streamDelta != nil {
			if event.streamDelta.Kind == chatStreamThinking {
				thought.WriteString(event.streamDelta.Content)
			} else {
				response.WriteString(event.streamDelta.Content)
			}
		}
		if event.activity != nil {
			actions = append(actions, event.activity.Action)
		}
		if event.response != nil {
			final = event.response
		}
		if event.err != nil {
			t.Fatal(event.err)
		}
	}
	if thought.String() != "thinking " || response.String() != "done" {
		t.Fatalf("streams: thought=%q response=%q", thought.String(), response.String())
	}
	if !slices.Equal(actions, []string{"pending", "completed"}) {
		t.Fatalf("tool actions = %#v", actions)
	}
	if final == nil || len(final.Choices) != 1 || final.Choices[0].Message.Content != "done" {
		t.Fatalf("final response = %#v", final)
	}
}

func TestACPRemoteClientResetClosesOldSession(t *testing.T) {
	connection := &fakeACPRemoteConnection{newSessionID: "new-session"}
	remote := &acpRemoteClient{
		connection: connection, sessionID: "old-session", root: t.TempDir(),
		capabilities: acp.AgentCapabilities{SessionCapabilities: acp.SessionCapabilities{Close: &acp.SessionCloseCapabilities{}}},
	}
	sessionID, err := remote.resetSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != "new-session" || !slices.Equal(connection.closed, []acp.SessionId{"old-session"}) || remote.sessionID != "new-session" {
		t.Fatalf("reset: id=%q closed=%#v remote=%q", sessionID, connection.closed, remote.sessionID)
	}
}

func TestACPRemoteClientResetCanRetryAfterNewSessionFailure(t *testing.T) {
	connection := &fakeACPRemoteConnection{newErr: errors.New("temporary new failure")}
	remote := &acpRemoteClient{
		connection: connection, sessionID: "old-session", root: t.TempDir(),
		capabilities: acp.AgentCapabilities{SessionCapabilities: acp.SessionCapabilities{Close: &acp.SessionCloseCapabilities{}}},
	}
	if _, err := remote.resetSession(t.Context()); err == nil {
		t.Fatal("first reset unexpectedly succeeded")
	}
	if remote.sessionID != "" || !slices.Equal(connection.closed, []acp.SessionId{"old-session"}) {
		t.Fatalf("failed reset: remote=%q closed=%#v", remote.sessionID, connection.closed)
	}
	connection.newErr = nil
	connection.newSessionID = "new-session"
	if sessionID, err := remote.resetSession(t.Context()); err != nil || sessionID != "new-session" {
		t.Fatalf("retry reset = %q, %v", sessionID, err)
	}
	if !slices.Equal(connection.closed, []acp.SessionId{"old-session"}) {
		t.Fatalf("retry re-closed old session: %#v", connection.closed)
	}
}

func TestChoiceOnlyQuestionDoesNotOfferCustomAnswer(t *testing.T) {
	input := askToUserInput{
		Question: "Allow?", ChoiceOnly: true,
		Choices: []askToUserChoice{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}},
	}
	if questionChoiceCount(input) != 2 || customAnswerSelected(input, 2) {
		t.Fatalf("choice-only selection behavior is incorrect")
	}
	if rendered := renderPendingQuestion(input, 0); strings.Contains(rendered, customAnswerLabel) {
		t.Fatalf("choice-only question rendered custom answer: %q", rendered)
	}
}

func TestACPReadOnlyPermissionAllowsSearchAndRejectsMutation(t *testing.T) {
	allow := acp.PermissionOption{OptionId: "allow", Name: "Allow", Kind: acp.PermissionOptionKindAllowOnce}
	search := acp.ToolKindSearch
	response := readOnlyPermission(acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{ToolCallId: "search", Kind: &search}, Options: []acp.PermissionOption{allow},
	})
	if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "allow" {
		t.Fatalf("search permission = %#v", response)
	}
	edit := acp.ToolKindEdit
	response = readOnlyPermission(acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{ToolCallId: "edit", Kind: &edit}, Options: []acp.PermissionOption{allow},
	})
	if response.Outcome.Cancelled == nil {
		t.Fatalf("edit permission = %#v", response)
	}
}
