package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/snowmerak/q/v2/pkg/domain"
	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
	"github.com/snowmerak/q/v2/pkg/memory"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/task"
)

const (
	maxToolRounds     = 12
	maxSubagentRounds = 6
)

var (
	ErrInvocationNotFound = errors.New("runtime: tool invocation not found")
	ErrQuestionNotFound   = errors.New("runtime: user question not found")
	ErrNotAwaiting        = errors.New("runtime: resource is not awaiting resolution")
)

type toolLoopOutcome int

const (
	toolLoopContinue toolLoopOutcome = iota
	toolLoopPaused
	toolLoopStopped
	toolLoopCutIn
	toolLoopFailed
)

func (e *Engine) SetTools(reg *task.Registry, dataDir string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.tools = reg
	e.dataDir = dataDir
	e.mu.Unlock()
}

func (e *Engine) currentTools() (*task.Registry, string) {
	e.mu.Lock()
	reg, dir := e.tools, e.dataDir
	e.mu.Unlock()
	return reg, dir
}

// ResolveApproval approves or rejects a pending tool invocation and resumes the paused turn.
func (e *Engine) ResolveApproval(ctx context.Context, roomID, invocationID, decision, reason string) (domain.ToolInvocation, error) {
	decision = strings.ToLower(strings.TrimSpace(decision))
	if decision != "approve" && decision != "reject" {
		return domain.ToolInvocation{}, fmt.Errorf("%w: decision must be approve or reject", ErrInvalidInput)
	}
	reason = strings.TrimSpace(reason)

	inv, err := e.rdb.Tools().GetInvocation(ctx, invocationID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.ToolInvocation{}, ErrInvocationNotFound
		}
		return domain.ToolInvocation{}, err
	}
	if inv.RoomID != roomID {
		return domain.ToolInvocation{}, ErrInvocationNotFound
	}
	if inv.Status != domain.ToolStatusPendingApproval {
		return domain.ToolInvocation{}, fmt.Errorf("%w: invocation status is %s", ErrNotAwaiting, inv.Status)
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return domain.ToolInvocation{}, errors.New("runtime: engine is closed")
	}
	turn, err := e.rdb.Turns().Get(ctx, inv.TurnID)
	if err != nil {
		e.mu.Unlock()
		return domain.ToolInvocation{}, err
	}
	if turn.Status != domain.TurnStatusAwaitingApproval {
		e.mu.Unlock()
		return domain.ToolInvocation{}, fmt.Errorf("%w: turn status is %s", ErrNotAwaiting, turn.Status)
	}

	specCtx, ok := e.hub.StartTurn(roomID, turn.ID)
	if !ok {
		e.mu.Unlock()
		return domain.ToolInvocation{}, ErrRoomBusy
	}

	var cp turnCheckpoint
	_ = json.Unmarshal([]byte(turn.CheckpointJSON), &cp)
	if decision == "approve" {
		inv.Status = domain.ToolStatusApproved
		inv.DecisionReason = reason
		cp.PendingInvocationID = inv.ID
	} else {
		inv.Status = domain.ToolStatusRejected
		inv.DecisionReason = reason
		inv.ErrorMessage = "rejected by user"
		msg := "Tool call rejected by user."
		if reason != "" {
			msg += " Reason: " + reason
		}
		cp.ToolHistory = append(cp.ToolHistory, llmprovider.Message{
			Role:       llmprovider.RoleTool,
			ToolCallID: inv.CallID,
			Content:    msg,
		})
		cp.RemainingCalls = dropToolCallByID(cp.RemainingCalls, inv.CallID)
		cp.PendingInvocationID = ""
	}
	cp.Phase = "resumed_approval"
	cpBytes, _ := json.Marshal(cp)
	turn.CheckpointJSON = string(cpBytes)
	turn.Status = domain.TurnStatusRunning
	turn.ErrorCode = ""
	turn.ErrorMessage = ""
	turn.CompletedAt = nil

	err = e.rdb.Tx(ctx, func(tx storage.RDBTx) error {
		if err := tx.Tools().UpdateInvocation(ctx, inv); err != nil {
			return err
		}
		return tx.Turns().Update(ctx, turn)
	})
	e.mu.Unlock()

	if err != nil {
		e.hub.FinishTurn(roomID, turn.ID)
		return domain.ToolInvocation{}, err
	}

	e.events.Publish(roomID, "approval.resolved", map[string]any{
		"turn_id":       turn.ID,
		"invocation_id": inv.ID,
		"decision":      decision,
	})
	e.launchTurn(roomID, turn, specCtx)
	return inv, nil
}

// ResolveQuestion records a user's answer to ask_to_user and resumes the paused turn.
func (e *Engine) ResolveQuestion(ctx context.Context, roomID, questionID, answerText string) (domain.UserQuestion, error) {
	answerText = strings.TrimSpace(answerText)
	if answerText == "" {
		return domain.UserQuestion{}, fmt.Errorf("%w: answer_text is required", ErrInvalidInput)
	}

	q, err := e.rdb.Tools().GetQuestion(ctx, questionID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.UserQuestion{}, ErrQuestionNotFound
		}
		return domain.UserQuestion{}, err
	}
	if q.RoomID != roomID {
		return domain.UserQuestion{}, ErrQuestionNotFound
	}
	if q.Status != domain.QuestionStatusPending {
		return domain.UserQuestion{}, fmt.Errorf("%w: question status is %s", ErrNotAwaiting, q.Status)
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return domain.UserQuestion{}, errors.New("runtime: engine is closed")
	}
	turn, err := e.rdb.Turns().Get(ctx, q.TurnID)
	if err != nil {
		e.mu.Unlock()
		return domain.UserQuestion{}, err
	}
	if turn.Status != domain.TurnStatusAwaitingUser {
		e.mu.Unlock()
		return domain.UserQuestion{}, fmt.Errorf("%w: turn status is %s", ErrNotAwaiting, turn.Status)
	}

	specCtx, ok := e.hub.StartTurn(roomID, turn.ID)
	if !ok {
		e.mu.Unlock()
		return domain.UserQuestion{}, ErrRoomBusy
	}

	now := domain.NowMS()
	q.Status = domain.QuestionStatusAnswered
	q.AnswerText = answerText
	q.AnsweredAt = &now

	var cp turnCheckpoint
	_ = json.Unmarshal([]byte(turn.CheckpointJSON), &cp)
	cp.ToolHistory = append(cp.ToolHistory, llmprovider.Message{
		Role:       llmprovider.RoleTool,
		ToolCallID: cp.PendingToolCallID,
		Content:    answerText,
	})
	cp.RemainingCalls = dropToolCallByID(cp.RemainingCalls, cp.PendingToolCallID)

	var pendingInvID = cp.PendingInvocationID
	cp.PendingInvocationID = ""
	cp.PendingQuestionID = ""
	cp.PendingToolCallID = ""
	cp.Phase = "resumed_question"
	cpBytes, _ := json.Marshal(cp)
	turn.CheckpointJSON = string(cpBytes)
	turn.Status = domain.TurnStatusRunning
	turn.ErrorCode = ""
	turn.ErrorMessage = ""
	turn.CompletedAt = nil

	err = e.rdb.Tx(ctx, func(tx storage.RDBTx) error {
		if err := tx.Tools().UpdateQuestion(ctx, q); err != nil {
			return err
		}
		if pendingInvID != "" {
			if inv, getErr := tx.Tools().GetInvocation(ctx, pendingInvID); getErr == nil {
				inv.Status = domain.ToolStatusCompleted
				inv.ResultSummary = truncateSummary(answerText, 1000)
				_ = tx.Tools().UpdateInvocation(ctx, inv)
			}
		}
		return tx.Turns().Update(ctx, turn)
	})
	e.mu.Unlock()

	if err != nil {
		e.hub.FinishTurn(roomID, turn.ID)
		return domain.UserQuestion{}, err
	}

	e.events.Publish(roomID, "question.resolved", map[string]any{
		"turn_id":     turn.ID,
		"question_id": q.ID,
		"answer_text": answerText,
	})
	e.launchTurn(roomID, turn, specCtx)
	return q, nil
}

func (e *Engine) processToolCalls(
	baseCtx context.Context,
	specCtx context.Context,
	roomID string,
	turn *domain.Turn,
	cp *turnCheckpoint,
	room domain.Room,
	members []domain.RoomMember,
	member domain.RoomMember,
	snap ProviderSnapshot,
	models ResolvedModels,
	latestUserText string,
	toolReg *task.Registry,
	dataDir string,
) (toolLoopOutcome, context.Context) {
	var decEval memory.DecisionEvaluator
	if snap.Decisions != nil {
		decEval = snap.Decisions
	}

	for len(cp.RemainingCalls) > 0 {
		if interrupted, nextCtx := e.handlePostCancel(baseCtx, roomID, turn, room, members); interrupted == interceptStop {
			return toolLoopStopped, nil
		} else if interrupted == interceptCutIn {
			return toolLoopCutIn, nextCtx
		}

		tc := cp.RemainingCalls[0]
		spec, specFound := toolReg.Lookup(tc.Function.Name)
		if !specFound {
			spec = task.ToolSpec{Name: tc.Function.Name, Source: domain.ToolSourceBuiltin, Risk: task.RiskExternal}
		}

		effectiveMode := task.EffectiveMode(
			snap.Config.Tools.ApprovalMode,
			room.ApprovalModeOverride,
			member.CharacterSnapshot.ApprovalPolicyOverride,
			snap.Config.Tools.ToolOverrides,
			tc.Function.Name,
		)

		var inv domain.ToolInvocation
		resumedApproved := false
		if cp.PendingInvocationID != "" {
			loaded, err := e.rdb.Tools().GetInvocation(baseCtx, cp.PendingInvocationID)
			if err == nil && loaded.CallID == tc.ID && loaded.Status == domain.ToolStatusApproved {
				inv = loaded
				resumedApproved = true
			}
		}

		if !resumedApproved {
			needsApproval := false
			reason := "unknown tool"
			if specFound {
				needsApproval, reason = task.EvaluateApproval(specCtx, decEval, models.DecisionModel, effectiveMode, spec, tc.Function.Arguments, latestUserText)
			}
			inv = domain.ToolInvocation{
				ID:             domain.NewID(),
				TurnID:         turn.ID,
				RoomID:         roomID,
				ActorID:        member.CharacterID,
				CallID:         tc.ID,
				ToolName:       tc.Function.Name,
				ToolSource:     spec.Source,
				ArgsJSON:       tc.Function.Arguments,
				GateMode:       effectiveMode,
				DecisionReason: reason,
				Status:         domain.ToolStatusRunning,
				CreatedAt:      domain.NowMS(),
				UpdatedAt:      domain.NowMS(),
			}
			if needsApproval {
				inv.Status = domain.ToolStatusPendingApproval
				cp.PendingInvocationID = inv.ID
				cp.Phase = "awaiting_approval"
				cpBytes, _ := json.Marshal(cp)
				turn.CheckpointJSON = string(cpBytes)
				turn.Status = domain.TurnStatusAwaitingApproval

				if err := e.rdb.Tx(baseCtx, func(tx storage.RDBTx) error {
					if err := tx.Tools().CreateInvocation(baseCtx, inv); err != nil {
						return err
					}
					return tx.Turns().Update(baseCtx, *turn)
				}); err != nil {
					e.finishFailed(baseCtx, roomID, turn, "internal", err.Error(), true)
					return toolLoopFailed, nil
				}
				e.events.Publish(roomID, "approval.required", map[string]any{
					"turn_id":    turn.ID,
					"invocation": inv,
				})
				return toolLoopPaused, nil
			}
			if err := e.rdb.Tools().CreateInvocation(baseCtx, inv); err != nil {
				e.finishFailed(baseCtx, roomID, turn, "internal", err.Error(), true)
				return toolLoopFailed, nil
			}
		} else {
			inv.Status = domain.ToolStatusRunning
			_ = e.rdb.Tools().UpdateInvocation(baseCtx, inv)
		}

		e.events.Publish(roomID, "actor.status", map[string]any{
			"turn_id":    turn.ID,
			"actor_id":   member.CharacterID,
			"actor_name": member.CharacterSnapshot.Name,
			"phase":      "acting",
		})
		e.events.Publish(roomID, "tool.started", map[string]any{
			"turn_id":       turn.ID,
			"invocation_id": inv.ID,
			"actor_id":      member.CharacterID,
			"tool_name":     inv.ToolName,
			"gate_mode":     string(inv.GateMode),
		})

		callCtx := task.CallContext{
			RoomID:             roomID,
			TurnID:             turn.ID,
			ActorID:            member.CharacterID,
			ActorName:          member.CharacterSnapshot.Name,
			UserRequest:        latestUserText,
			ApprovalMode:       effectiveMode,
			AllowedRoots:       snap.Config.Tools.AllowedRoots,
			DataDir:            dataDir,
			DelegationDepth:    0,
			MaxDelegationDepth: snap.Config.Tools.MaxDelegationDepth,
			ProgressFn: func(msg string) {
				e.events.Publish(roomID, "tool.progress", map[string]any{
					"turn_id":       turn.ID,
					"invocation_id": inv.ID,
					"message":       msg,
				})
			},
			SubagentFn: func(subCtx context.Context, subPrompt, role string, depth int) (string, error) {
				return e.runSubagentLoop(subCtx, snap, models, room, member, toolReg, dataDir, subPrompt, role, depth)
			},
		}

		out, execErr := toolReg.Execute(specCtx, callCtx, tc.Function.Name, tc.Function.Arguments)

		if interrupted, nextCtx := e.handlePostCancel(baseCtx, roomID, turn, room, members); interrupted == interceptStop {
			inv.Status = domain.ToolStatusCancelled
			_ = e.rdb.Tools().UpdateInvocation(baseCtx, inv)
			return toolLoopStopped, nil
		} else if interrupted == interceptCutIn {
			inv.Status = domain.ToolStatusCancelled
			_ = e.rdb.Tools().UpdateInvocation(baseCtx, inv)
			return toolLoopCutIn, nextCtx
		}

		if out.PauseKind == "awaiting_user" && out.UserQuestion != nil {
			q := *out.UserQuestion
			cp.PendingQuestionID = q.ID
			cp.PendingToolCallID = tc.ID
			cp.PendingInvocationID = inv.ID
			cp.Phase = "awaiting_user"
			cpBytes, _ := json.Marshal(cp)
			turn.CheckpointJSON = string(cpBytes)
			turn.Status = domain.TurnStatusAwaitingUser

			if err := e.rdb.Tx(baseCtx, func(tx storage.RDBTx) error {
				if err := tx.Tools().CreateQuestion(baseCtx, q); err != nil {
					return err
				}
				return tx.Turns().Update(baseCtx, *turn)
			}); err != nil {
				e.finishFailed(baseCtx, roomID, turn, "internal", err.Error(), true)
				return toolLoopFailed, nil
			}
			e.events.Publish(roomID, "question.required", map[string]any{
				"turn_id":  turn.ID,
				"question": q,
			})
			return toolLoopPaused, nil
		}

		if execErr != nil {
			out = task.ToolOutput{Content: execErr.Error(), IsError: true}
		}
		if out.IsError {
			inv.Status = domain.ToolStatusFailed
			inv.ErrorMessage = truncateSummary(out.Content, 1000)
		} else {
			inv.Status = domain.ToolStatusCompleted
		}
		inv.ResultSummary = truncateSummary(out.Content, 1000)
		inv.ArtifactID = out.ArtifactID

		cp.ToolHistory = append(cp.ToolHistory, llmprovider.Message{
			Role:       llmprovider.RoleTool,
			ToolCallID: tc.ID,
			Content:    out.Content,
		})
		cp.RemainingCalls = cp.RemainingCalls[1:]
		cp.PendingInvocationID = ""
		cp.Phase = "tool_completed"
		cpBytes, _ := json.Marshal(cp)
		turn.CheckpointJSON = string(cpBytes)

		if err := e.rdb.Tx(baseCtx, func(tx storage.RDBTx) error {
			if err := tx.Tools().UpdateInvocation(baseCtx, inv); err != nil {
				return err
			}
			return tx.Turns().Update(baseCtx, *turn)
		}); err != nil {
			e.finishFailed(baseCtx, roomID, turn, "internal", err.Error(), true)
			return toolLoopFailed, nil
		}

		e.events.Publish(roomID, "tool.completed", map[string]any{
			"turn_id":    turn.ID,
			"invocation": inv,
		})
	}
	return toolLoopContinue, specCtx
}

func (e *Engine) runSubagentLoop(
	ctx context.Context,
	snap ProviderSnapshot,
	models ResolvedModels,
	room domain.Room,
	member domain.RoomMember,
	toolReg *task.Registry,
	dataDir string,
	subPrompt, role string,
	depth int,
) (string, error) {
	if snap.Gateway == nil {
		return "", errors.New("gateway unavailable")
	}
	subModel := strings.TrimSpace(snap.Config.LLM.Roles.SubagentModel)
	subEffort := strings.TrimSpace(snap.Config.LLM.Roles.SubagentReasoningEffort)
	if subModel == "" {
		subModel = models.ChatModel
		if subEffort == "" {
			subEffort = models.ReasoningEffort
		}
	}
	msgs := []llmprovider.Message{
		{
			Role:    llmprovider.RoleSystem,
			Content: fmt.Sprintf("You are subagent %q helping character %q complete a focused subtask concisely.", role, member.CharacterSnapshot.Name),
		},
		{
			Role:    llmprovider.RoleUser,
			Content: subPrompt,
		},
	}

	var decEval memory.DecisionEvaluator
	if snap.Decisions != nil {
		decEval = snap.Decisions
	}
	baseMode := task.EffectiveMode(snap.Config.Tools.ApprovalMode, room.ApprovalModeOverride, member.CharacterSnapshot.ApprovalPolicyOverride, nil, "")
	allowNested := depth < snap.Config.Tools.MaxDelegationDepth

	for round := 0; round < maxSubagentRounds; round++ {
		subCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		resp, err := snap.Gateway.Chat(subCtx, llmprovider.ChatRequest{
			Model:           subModel,
			Messages:        msgs,
			Tools:           toolReg.ToolDefinitions(allowNested),
			ReasoningEffort: subEffort,
		})
		cancel()
		if err != nil {
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", errors.New("subagent received empty response")
		}
		choice := resp.Choices[0].Message
		if len(choice.ToolCalls) == 0 {
			return strings.TrimSpace(choice.TextContent()), nil
		}
		msgs = append(msgs, choice)
		for _, tc := range choice.ToolCalls {
			spec, ok := toolReg.Lookup(tc.Function.Name)
			if !ok {
				msgs = append(msgs, llmprovider.Message{Role: llmprovider.RoleTool, ToolCallID: tc.ID, Content: "unknown tool"})
				continue
			}
			mode := task.EffectiveMode(baseMode, nil, nil, snap.Config.Tools.ToolOverrides, tc.Function.Name)
			needsApproval, reason := task.EvaluateApproval(ctx, decEval, models.DecisionModel, mode, spec, tc.Function.Arguments, subPrompt)
			if needsApproval {
				msgs = append(msgs, llmprovider.Message{
					Role:       llmprovider.RoleTool,
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf("Subagent blocked from executing %s (%s). Return control to parent character if user approval is required.", tc.Function.Name, reason),
				})
				continue
			}
			out, execErr := toolReg.Execute(ctx, task.CallContext{
				RoomID:             room.ID,
				ActorID:            member.CharacterID,
				ActorName:          role,
				UserRequest:        subPrompt,
				ApprovalMode:       mode,
				AllowedRoots:       snap.Config.Tools.AllowedRoots,
				DataDir:            dataDir,
				DelegationDepth:    depth,
				MaxDelegationDepth: snap.Config.Tools.MaxDelegationDepth,
				SubagentFn: func(sCtx context.Context, p, r string, d int) (string, error) {
					return e.runSubagentLoop(sCtx, snap, models, room, member, toolReg, dataDir, p, r, d)
				},
			}, tc.Function.Name, tc.Function.Arguments)
			if execErr != nil {
				out = task.ToolOutput{Content: execErr.Error(), IsError: true}
			}
			msgs = append(msgs, llmprovider.Message{
				Role:       llmprovider.RoleTool,
				ToolCallID: tc.ID,
				Content:    out.Content,
			})
		}
	}
	return "", errors.New("subagent exceeded max rounds")
}

func accumulateToolCallChunk(acc map[int]*llmprovider.ToolCall, fragments []llmprovider.ToolCall) {
	for _, tc := range fragments {
		entry, ok := acc[tc.Index]
		if !ok {
			copied := tc
			acc[tc.Index] = &copied
			continue
		}
		if tc.ID != "" {
			entry.ID = tc.ID
		}
		if tc.Type != "" {
			entry.Type = tc.Type
		}
		if tc.Function.Name != "" {
			entry.Function.Name += tc.Function.Name
		}
		if tc.Function.Arguments != "" {
			entry.Function.Arguments += tc.Function.Arguments
		}
	}
}

func finalizeToolCalls(acc map[int]*llmprovider.ToolCall) []llmprovider.ToolCall {
	if len(acc) == 0 {
		return nil
	}
	keys := make([]int, 0, len(acc))
	for k := range acc {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	out := make([]llmprovider.ToolCall, 0, len(keys))
	for _, k := range keys {
		tc := *acc[k]
		if tc.Type == "" {
			tc.Type = llmprovider.ToolTypeFunction
		}
		if tc.ID == "" {
			tc.ID = domain.NewID()
		}
		out = append(out, tc)
	}
	return out
}

func dropToolCallByID(calls []llmprovider.ToolCall, callID string) []llmprovider.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	if calls[0].ID == callID {
		return append([]llmprovider.ToolCall(nil), calls[1:]...)
	}
	out := make([]llmprovider.ToolCall, 0, len(calls))
	for _, c := range calls {
		if c.ID != callID {
			out = append(out, c)
		}
	}
	return out
}

func truncateSummary(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
