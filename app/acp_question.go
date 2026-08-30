package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

type acpPendingQuestion struct {
	input           askToUserInput
	answer          chan askToUserOutput
	planApproval    bool
	includeInMemory bool
	toolBacked      bool
	resume          func(context.Context) (acp.PromptResponse, error)
	abort           func() error
}

type acpPlanContinuation struct {
	workflowCtx context.Context
	cancel      context.CancelFunc
	events      <-chan agentEvent
	trace       *acpPlanTrace
	objective   string
	workflow    string
	detailed    bool
	finishOnce  sync.Once
	finishErr   error
}

func (run *acpPlanContinuation) workflowName() string {
	if run == nil || strings.TrimSpace(run.workflow) == "" {
		return "plan"
	}
	return strings.TrimSpace(run.workflow)
}

func (run *acpPlanContinuation) finish(
	a *acpAgent,
	ctx context.Context,
	response acp.PromptResponse,
	runErr error,
) error {
	if run == nil {
		return nil
	}
	run.finishOnce.Do(func() {
		run.cancel()
		name := strings.ToUpper(run.workflowName()[:1]) + run.workflowName()[1:]
		reason := name + " workflow ended before the tool returned a result."
		if runErr != nil {
			reason = name + " workflow stopped: " + runErr.Error()
		}
		if errors.Is(ctx.Err(), context.Canceled) || response.StopReason == acp.StopReasonCancelled {
			reason = name + " workflow cancelled before the tool returned a result."
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cleanupCancel()
		if run.trace != nil {
			run.finishErr = run.trace.finishPending(cleanupCtx, reason)
			if run.finishErr != nil && a.logger != nil {
				a.logger.Warn("finish ACP subagent tool calls", "error", run.finishErr)
			}
		}
	})
	return run.finishErr
}

func (a *acpAgent) suspendACPQuestion(
	ctx context.Context,
	input askToUserInput,
	answer chan askToUserOutput,
	planApproval bool,
	includeInMemory bool,
	toolBacked bool,
	resume func(context.Context) (acp.PromptResponse, error),
	abort func() error,
) (acp.PromptResponse, error) {
	if a.pendingQuestion != nil {
		if abort != nil {
			_ = abort()
		}
		return acp.PromptResponse{}, errors.New("ACP question is already pending")
	}
	pending := &acpPendingQuestion{
		input: input, answer: answer, planApproval: planApproval,
		includeInMemory: includeInMemory, toolBacked: toolBacked,
		resume: resume, abort: abort,
	}
	a.pendingQuestion = pending
	content := renderACPFreeformQuestion(input)
	if planApproval {
		content = renderACPPlanApprovalQuestion(input, false)
	}
	response, err := a.finishACPPendingQuestionPrompt(ctx, content, includeInMemory)
	if err != nil {
		a.pendingQuestion = nil
		if abort != nil {
			err = errors.Join(err, abort())
		}
	}
	return response, err
}

func (a *acpAgent) runPendingACPQuestion(
	ctx context.Context,
	message client.Message,
	hasNonText bool,
) (acp.PromptResponse, bool, error) {
	pending := a.pendingQuestion
	if pending == nil {
		return acp.PromptResponse{}, false, nil
	}
	if err := a.beginACPPendingQuestionInput(ctx, message, pending.includeInMemory); err != nil {
		return acp.PromptResponse{}, true, err
	}

	var answer askToUserOutput
	if pending.planApproval {
		action, valid := parseACPPlanApprovalAction(message.TextContent())
		if hasNonText || !valid {
			response, err := a.finishACPPendingQuestionPrompt(
				ctx,
				renderACPPlanApprovalQuestion(pending.input, true),
				pending.includeInMemory,
			)
			return response, true, err
		}
		answer.SelectedChoiceID = action
	} else {
		answer.Freeform = strings.TrimSpace(message.TextContent())
	}

	a.pendingQuestion = nil
	pending.answer <- answer
	if pending.resume == nil {
		if pending.abort != nil {
			_ = pending.abort()
		}
		return acp.PromptResponse{}, true, errors.New("ACP question cannot resume its workflow")
	}
	response, err := pending.resume(ctx)
	return response, true, err
}

func (a *acpAgent) beginACPPendingQuestionInput(
	ctx context.Context,
	message client.Message,
	includeInMemory bool,
) error {
	a.state.archiveMessage(message, sessionstore.StatusSubmitted, false)
	a.state.messages = append(a.state.messages, message)
	if includeInMemory {
		if a.state.memory == nil {
			a.state.memory = memory.New(memoryPolicy(a.state.activeConfig()), nil)
		}
		a.state.memory.Append(message)
	}
	a.launchLearning(a.state.observeLearningMessage(message))
	a.state.sessionUpdatedAt = time.Now().UTC()
	if err := a.state.saveWorkspaceSession(); err != nil {
		return err
	}
	return a.emitSessionInfoContext(ctx, false)
}

func (a *acpAgent) finishACPPendingQuestionPrompt(
	ctx context.Context,
	content string,
	includeInMemory bool,
) (acp.PromptResponse, error) {
	if err := a.updateContext(ctx, acp.UpdateAgentMessageText(content)); err != nil {
		return acp.PromptResponse{}, err
	}
	message := client.Message{Role: client.RoleAssistant, Content: content}
	a.state.messages = append(a.state.messages, message)
	if includeInMemory {
		if a.state.memory == nil {
			a.state.memory = memory.New(memoryPolicy(a.state.activeConfig()), nil)
		}
		a.state.memory.Append(message)
	}
	a.state.archiveMessage(message, sessionstore.StatusSucceeded, false)
	a.state.sessionUpdatedAt = time.Now().UTC()
	if err := a.state.saveWorkspaceSession(); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.emitSessionInfoContext(ctx, false); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.state.flushArchive(); err != nil && a.logger != nil {
		a.logger.Error("flush ACP pending question", "error", err)
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *acpAgent) closePendingACPQuestion() error {
	pending := a.pendingQuestion
	a.pendingQuestion = nil
	if pending == nil {
		return nil
	}
	var abortErr error
	if pending.abort != nil {
		abortErr = pending.abort()
	}
	if pending.toolBacked {
		a.state.completeInterruptedToolCalls()
		a.state.sessionUpdatedAt = time.Now().UTC()
		abortErr = errors.Join(abortErr, a.state.saveWorkspaceSession(), a.state.flushArchive())
	}
	return abortErr
}

func parseACPPlanApprovalAction(input string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "1", "1. approve", "approve":
		return "approve", true
	case "2", "2. revise", "revise":
		return "revise", true
	case "3", "3. cancel", "cancel":
		return "cancel", true
	default:
		return "", false
	}
}

func renderACPPlanApprovalQuestion(input askToUserInput, invalid bool) string {
	var body strings.Builder
	if invalid {
		body.WriteString("Invalid plan action. Reply with one of the numbered or English actions below.\n\n")
	}
	if input.Context != "" {
		body.WriteString(strings.TrimSpace(input.Context))
		body.WriteString("\n\n")
	}
	body.WriteString("Choose one action:\n")
	body.WriteString("1. approve — execute the proposed plan\n")
	body.WriteString("2. revise — regenerate the plan\n")
	body.WriteString("3. cancel — stop without execution\n\n")
	body.WriteString("Reply with the number or the English action.")
	return body.String()
}

func renderACPFreeformQuestion(input askToUserInput) string {
	var body strings.Builder
	body.WriteString(strings.TrimSpace(input.Question))
	if input.Context != "" {
		body.WriteString("\n\n")
		body.WriteString(strings.TrimSpace(input.Context))
	}
	if len(input.Choices) > 0 {
		body.WriteString("\n\nSuggestions:")
		for _, choice := range input.Choices {
			fmt.Fprintf(&body, "\n- %s", choice.Label)
			if choice.Description != "" {
				body.WriteString(" — ")
				body.WriteString(choice.Description)
			}
		}
	}
	body.WriteString("\n\nReply with your answer.")
	return body.String()
}
