package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/commitagent"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

const maximumACPCommitProposal = 64 << 10

type acpCommitSession interface {
	Proposals() []commitagent.Proposal
	AutoStaged() bool
	Regenerate(context.Context) error
	Commit(context.Context) (commitagent.Result, error)
	Push(context.Context) error
	Close() error
}

type acpCommitSessionFactory func(context.Context, commitagent.ProgressFunc) (acpCommitSession, error)

type acpPendingCommit struct {
	session acpCommitSession
}

func newACPCommitSessionFactory(state *model, root string) acpCommitSessionFactory {
	return func(ctx context.Context, progress commitagent.ProgressFunc) (acpCommitSession, error) {
		return commitagent.PrepareHeadlessWithConfig(
			ctx, root, state.workspaceLock, state.activeConfig(), progress,
		)
	}
}

func (a *acpAgent) runACPCommit(ctx context.Context) (response acp.PromptResponse, runErr error) {
	_, capabilities := a.connectionState()
	supportsForm := capabilities.Elicitation != nil && capabilities.Elicitation.Form != nil
	if a.commitSession == nil {
		return acp.PromptResponse{}, errors.New("ACP commit workflow is unavailable")
	}

	progress := func(event commitagent.ProgressEvent) {
		line := fmt.Sprintf("commit · %s · %s\n", event.Stage, event.Message)
		_ = a.update(acp.UpdateAgentThoughtText(line))
	}
	session, err := a.commitSession(ctx, progress)
	if err != nil {
		if errors.Is(err, commitagent.ErrNothingToCommit) {
			if updateErr := a.updateContext(ctx, acp.UpdateAgentMessageText("Nothing to commit.")); updateErr != nil {
				return acp.PromptResponse{}, updateErr
			}
			return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
		}
		return acp.PromptResponse{}, err
	}
	if err := a.beginACPCommitTurn(); err != nil {
		_ = session.Close()
		return acp.PromptResponse{}, err
	}
	defer func() {
		if runErr != nil {
			a.state.archiveFailure("ACP commit failed", runErr)
			_ = a.state.flushArchive()
		}
	}()
	if !supportsForm {
		proposal, err := renderACPCommitProposal(session.Proposals(), session.AutoStaged())
		if err != nil {
			_ = session.Close()
			return acp.PromptResponse{}, err
		}
		a.pendingCommit = &acpPendingCommit{session: session}
		response, err := a.finishACPCommit(renderACPCommitFallbackPrompt(proposal, false))
		if err != nil {
			_ = a.closePendingACPCommit()
			return acp.PromptResponse{}, err
		}
		return response, nil
	}
	defer func() {
		if closeErr := session.Close(); closeErr != nil {
			if runErr != nil {
				runErr = errors.Join(runErr, closeErr)
			} else if a.logger != nil {
				a.logger.Error("close ACP commit session", "error", closeErr)
			}
		}
	}()

	for {
		proposal, err := renderACPCommitProposal(session.Proposals(), session.AutoStaged())
		if err != nil {
			return acp.PromptResponse{}, err
		}
		action, err := a.elicitACPCommitAction(ctx, proposal)
		if err != nil {
			return acp.PromptResponse{}, err
		}
		switch action {
		case "regenerate":
			if err := session.Regenerate(ctx); err != nil {
				return acp.PromptResponse{}, err
			}
			continue
		case "cancel":
			return a.finishACPCommit("Commit cancelled. No commit or push was performed.")
		case "commit", "commit_push":
			result, err := session.Commit(ctx)
			if err != nil {
				return acp.PromptResponse{}, err
			}
			pushed := false
			var pushErr error
			if action == "commit_push" {
				pushErr = session.Push(ctx)
				pushed = pushErr == nil
			}
			return a.finishACPCommit(renderACPCommitResult(result, pushed, pushErr))
		default:
			return acp.PromptResponse{}, fmt.Errorf("unsupported ACP commit action %q", action)
		}
	}
}

func (a *acpAgent) runPendingACPCommit(
	ctx context.Context,
	message client.Message,
	hasNonText bool,
) (response acp.PromptResponse, handled bool, runErr error) {
	pending := a.pendingCommit
	if pending == nil {
		return acp.PromptResponse{}, false, nil
	}
	handled = true
	if err := a.beginACPCommitInputTurn(message); err != nil {
		_ = a.closePendingACPCommit()
		return acp.PromptResponse{}, true, err
	}
	defer func() {
		if runErr == nil {
			return
		}
		runErr = errors.Join(runErr, a.closePendingACPCommit())
		a.state.archiveFailure("ACP commit failed", runErr)
		_ = a.state.flushArchive()
	}()

	action, valid := parseACPCommitFallbackAction(message.TextContent())
	if hasNonText || !valid {
		proposal, err := renderACPCommitProposal(pending.session.Proposals(), pending.session.AutoStaged())
		if err != nil {
			return acp.PromptResponse{}, true, err
		}
		response, err := a.finishACPCommit(renderACPCommitFallbackPrompt(proposal, true))
		return response, true, err
	}

	switch action {
	case "regenerate":
		if err := pending.session.Regenerate(ctx); err != nil {
			return acp.PromptResponse{}, true, err
		}
		proposal, err := renderACPCommitProposal(pending.session.Proposals(), pending.session.AutoStaged())
		if err != nil {
			return acp.PromptResponse{}, true, err
		}
		response, err := a.finishACPCommit(renderACPCommitFallbackPrompt(proposal, false))
		return response, true, err
	case "cancel":
		a.finishPendingACPCommitSession()
		response, err := a.finishACPCommit("Commit cancelled. No commit or push was performed.")
		return response, true, err
	case "commit", "commit_push":
		result, err := pending.session.Commit(ctx)
		if err != nil {
			return acp.PromptResponse{}, true, err
		}
		pushed := false
		var pushErr error
		if action == "commit_push" {
			pushErr = pending.session.Push(ctx)
			pushed = pushErr == nil
		}
		a.finishPendingACPCommitSession()
		response, err := a.finishACPCommit(renderACPCommitResult(result, pushed, pushErr))
		return response, true, err
	default:
		return acp.PromptResponse{}, true, fmt.Errorf("unsupported ACP commit action %q", action)
	}
}

func (a *acpAgent) beginACPCommitInputTurn(message client.Message) error {
	a.state.turnMessageStart = len(a.state.messages)
	a.state.archiveMessage(message, sessionstore.StatusSubmitted, false)
	a.state.messages = append(a.state.messages, message)
	if a.state.memory == nil {
		a.state.memory = memory.New(memoryPolicy(a.state.activeConfig()), nil)
	}
	a.state.memory.Append(message)
	a.launchLearning(a.state.observeLearningMessage(message))
	if err := a.state.saveWorkspaceSession(); err != nil {
		return err
	}
	return a.emitSessionInfo(false)
}

func (a *acpAgent) closePendingACPCommit() error {
	pending := a.pendingCommit
	a.pendingCommit = nil
	if pending == nil || pending.session == nil {
		return nil
	}
	return pending.session.Close()
}

func (a *acpAgent) finishPendingACPCommitSession() {
	if err := a.closePendingACPCommit(); err != nil && a.logger != nil {
		a.logger.Error("close ACP commit session", "error", err)
	}
}

func (a *acpAgent) beginACPCommitTurn() error {
	a.state.turnMessageStart = len(a.state.messages)
	titleChanged := a.state.touchSessionMetadata("Commit changes")
	message := client.Message{Role: client.RoleUser, Content: "/commit"}
	a.state.archiveMessage(message, sessionstore.StatusSubmitted, false)
	a.state.messages = append(a.state.messages, message)
	if a.state.memory == nil {
		a.state.memory = memory.New(memoryPolicy(a.state.activeConfig()), nil)
	}
	a.state.memory.Append(message)
	a.launchLearning(a.state.observeLearningMessage(message))
	if err := a.state.saveWorkspaceSession(); err != nil {
		return err
	}
	return a.emitSessionInfo(titleChanged)
}

func (a *acpAgent) finishACPCommit(content string) (acp.PromptResponse, error) {
	streamedResponse := ""
	return a.finishPrompt(client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
		Role: client.RoleAssistant, Content: content,
	}}}}, 0, &streamedResponse)
}

func (a *acpAgent) elicitACPCommitAction(ctx context.Context, proposal string) (string, error) {
	connection, _ := a.connectionState()
	sessionID := a.sessionID
	if connection == nil {
		return "", errors.New("ACP connection is unavailable")
	}
	response, err := connection.UnstableCreateElicitation(ctx, acp.UnstableCreateElicitationRequest{
		Form: &acp.UnstableCreateElicitationForm{
			Message:   proposal,
			Mode:      "form",
			SessionId: sessionID,
			RequestedSchema: acp.UnstableElicitationSchema{
				Type:  acp.UnstableElicitationSchemaTypeObject,
				Title: acp.Ptr("Commit proposal"),
				Properties: map[string]any{
					"action": map[string]any{
						"type":        "string",
						"title":       "Action",
						"description": "Choose exactly one action after reviewing every proposed commit and file.",
						"oneOf": []map[string]any{
							{"const": "commit", "title": "Commit"},
							{"const": "commit_push", "title": "Commit and push"},
							{"const": "regenerate", "title": "Regenerate proposal"},
							{"const": "cancel", "title": "Cancel"},
						},
					},
				},
				Required: []string{"action"},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("ACP commit elicitation failed: %w", err)
	}
	if response.Decline != nil || response.Cancel != nil {
		return "cancel", nil
	}
	if response.Accept == nil {
		return "", errors.New("ACP commit elicitation returned no outcome")
	}
	action, ok := response.Accept.Content["action"].(string)
	if !ok || strings.TrimSpace(action) == "" {
		return "", errors.New("ACP commit elicitation returned no action")
	}
	return strings.TrimSpace(action), nil
}

func parseACPCommitFallbackAction(input string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "1", "1. approve", "approve", "commit":
		return "commit", true
	case "2", "2. approve-and-push", "approve-and-push", "approve_push", "commit_push", "push":
		return "commit_push", true
	case "3", "3. revise", "revise", "regenerate":
		return "regenerate", true
	case "4", "4. cancel", "cancel":
		return "cancel", true
	default:
		return "", false
	}
}

func renderACPCommitFallbackPrompt(proposal string, invalid bool) string {
	var body strings.Builder
	if invalid {
		body.WriteString("Invalid commit action. Reply with one of the numbered or English actions below.\n\n")
	}
	body.WriteString(proposal)
	body.WriteString("\n\nChoose one action:\n")
	body.WriteString("1. approve — create the proposed commit(s)\n")
	body.WriteString("2. approve-and-push — create the commit(s), then push\n")
	body.WriteString("3. revise — regenerate the proposal\n")
	body.WriteString("4. cancel — do nothing\n\n")
	body.WriteString("Reply with the number or the English action.")
	return body.String()
}

func renderACPCommitProposal(proposals []commitagent.Proposal, autoStaged bool) (string, error) {
	if len(proposals) == 0 {
		return "", errors.New("q commit: proposal is empty")
	}
	var body strings.Builder
	body.WriteString("Review the proposed commit messages and their staged files.\n")
	if autoStaged {
		body.WriteString("\nNote: the Git index was initially empty, so q staged all working-tree changes before generating this proposal. Cancelling leaves them staged.\n")
	}
	for index, proposal := range proposals {
		body.WriteString(fmt.Sprintf("\n%d. %s\n", index+1, acpCommitSubject(proposal)))
		for _, line := range proposal.Body {
			body.WriteString("   - ")
			body.WriteString(line)
			body.WriteByte('\n')
		}
		body.WriteString("   Files:\n")
		for _, path := range proposal.Files {
			body.WriteString("   - ")
			body.WriteString(path)
			body.WriteByte('\n')
		}
	}
	result := strings.TrimSpace(body.String())
	if len(result) > maximumACPCommitProposal {
		return "", fmt.Errorf("q commit: proposal exceeds the ACP review limit of %d bytes; use the TUI", maximumACPCommitProposal)
	}
	return result, nil
}

func acpCommitSubject(proposal commitagent.Proposal) string {
	prefix := proposal.Type
	if proposal.Scope != "" {
		prefix += "(" + proposal.Scope + ")"
	}
	return prefix + ": " + proposal.Summary
}

func renderACPCommitResult(result commitagent.Result, pushed bool, pushErr error) string {
	var body strings.Builder
	if len(result.Messages) == 1 {
		body.WriteString("Commit created.\n")
	} else {
		body.WriteString(fmt.Sprintf("%d commits created.\n", len(result.Messages)))
	}
	for _, message := range result.Messages {
		body.WriteString("\n- ")
		body.WriteString(strings.TrimSpace(strings.SplitN(message, "\n", 2)[0]))
	}
	if pushed {
		body.WriteString("\n\nPush completed.")
	} else if pushErr != nil {
		body.WriteString("\n\nCommit succeeded, but push failed: ")
		body.WriteString(pushErr.Error())
	}
	return strings.TrimSpace(body.String())
}
