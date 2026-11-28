package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/snowmerak/q/v2/pkg/app/config"
	"github.com/snowmerak/q/v2/pkg/domain"
	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
	"github.com/snowmerak/q/v2/pkg/llm/decisions"
	"github.com/snowmerak/q/v2/pkg/llm/gateway"
	"github.com/snowmerak/q/v2/pkg/memory"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/outbox"
	"github.com/snowmerak/q/v2/pkg/task"
)

var (
	ErrInvalidInput        = errors.New("runtime: invalid input")
	ErrRoomNotFound        = errors.New("runtime: room not found")
	ErrTurnNotFound        = errors.New("runtime: turn not found")
	ErrRoomBusy            = errors.New("runtime: room already has an active turn")
	ErrIdempotencyConflict = errors.New("runtime: request_id already exists with different input")
	ErrTurnNotRetryable    = errors.New("runtime: turn cannot be retried in its current status")
)

// ProviderSnapshot holds a point-in-time view of dynamically hot-reloadable backends.
type ProviderSnapshot struct {
	Config    config.Config
	Gateway   *gateway.Gateway
	Decisions *decisions.Registry
	Embedder  memory.QueryEmbedder
}

// ProviderSource provides thread-safe access to the latest ProviderSnapshot.
type ProviderSource interface {
	Snapshot() ProviderSnapshot
}

type SubmitTurnRequest struct {
	RequestID   string `json:"request_id"`
	Text        string `json:"text"`
	LocationTag string `json:"location_tag,omitempty"`
}

type SubmitTurnResult struct {
	Turn             domain.Turn
	UserMessageID    string
	AlreadyCompleted bool
	AlreadyRunning   bool
}

type turnCheckpoint struct {
	UserMessageID       string                 `json:"user_message_id,omitempty"`
	ActorID             string                 `json:"actor_id,omitempty"`
	Phase               string                 `json:"phase,omitempty"`
	ToolHistory         []llmprovider.Message  `json:"tool_history,omitempty"`
	PendingInvocationID string                 `json:"pending_invocation_id,omitempty"`
	PendingQuestionID   string                 `json:"pending_question_id,omitempty"`
	PendingToolCallID   string                 `json:"pending_tool_call_id,omitempty"`
	RemainingCalls      []llmprovider.ToolCall `json:"remaining_calls,omitempty"`
	ToolRounds          int                    `json:"tool_rounds,omitempty"`
	GroupGauges         map[string]float64     `json:"group_gauges,omitempty"`
	GroupWaited         []string               `json:"group_waited,omitempty"`
	GroupSteps          int                    `json:"group_steps,omitempty"`
}

// Engine executes direct and group character turns, broadcasts SSE events, and
// schedules post-commit memory extraction and emotional state appraisal.
type Engine struct {
	rdb       storage.RDBStore
	search    storage.SearchIndex
	providers ProviderSource
	logger    *slog.Logger
	hub       *InterceptHub
	events    *EventBus

	bgWG    sync.WaitGroup
	mu      sync.Mutex
	tools   *task.Registry
	dataDir string
	closed  bool
}

func NewEngine(rdb storage.RDBStore, search storage.SearchIndex, providers ProviderSource, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{
		rdb:       rdb,
		search:    search,
		providers: providers,
		logger:    logger,
		hub:       NewInterceptHub(),
		events:    NewEventBus(),
	}
}

func (e *Engine) Events() *EventBus  { return e.events }
func (e *Engine) Hub() *InterceptHub { return e.hub }

// Close stops all active turns and waits for in-flight turn & background goroutines.
func (e *Engine) Close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()

	e.hub.StopAll()
	e.bgWG.Wait()
}

// SubmitTurn validates input, checks (room_id, request_id) idempotency, commits the
// user message, and launches asynchronous character execution for the turn.
func (e *Engine) SubmitTurn(ctx context.Context, roomID string, req SubmitTurnRequest) (SubmitTurnResult, error) {
	req.RequestID = strings.TrimSpace(req.RequestID)
	req.Text = strings.TrimSpace(req.Text)
	req.LocationTag = strings.TrimSpace(req.LocationTag)
	if req.RequestID == "" || req.Text == "" {
		return SubmitTurnResult{}, fmt.Errorf("%w: request_id and text are required", ErrInvalidInput)
	}

	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return SubmitTurnResult{}, errors.New("runtime: engine is closed")
	}

	room, err := e.rdb.Rooms().Get(ctx, roomID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return SubmitTurnResult{}, ErrRoomNotFound
		}
		return SubmitTurnResult{}, err
	}
	if room.Mode != domain.RoomModeDirect && room.Mode != domain.RoomModeGroup {
		return SubmitTurnResult{}, fmt.Errorf("%w: unsupported room mode %q", ErrInvalidInput, room.Mode)
	}
	members, err := e.rdb.Rooms().ListMembers(ctx, roomID)
	if err != nil {
		return SubmitTurnResult{}, err
	}
	var activeMember *domain.RoomMember
	for i := range members {
		if members[i].Active {
			activeMember = &members[i]
			break
		}
	}
	if activeMember == nil {
		return SubmitTurnResult{}, fmt.Errorf("%w: room has no active character", ErrInvalidInput)
	}

	inputHash := domain.HashInput(req.Text + "\x00" + req.LocationTag)

	var (
		turn          domain.Turn
		created       bool
		userMessageID string
		specCtx       context.Context
		acquiredSlot  bool
	)

	// Serialize room slot checks safely without holding lock during LLM calls.
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return SubmitTurnResult{}, errors.New("runtime: engine is closed")
	}
	err = e.rdb.Tx(ctx, func(tx storage.RDBTx) error {
		t, wasCreated, err := tx.Turns().CreateOrGetIdempotent(ctx, domain.Turn{
			RoomID:    roomID,
			RequestID: req.RequestID,
			InputHash: inputHash,
			Status:    domain.TurnStatusRunning,
		})
		if err != nil {
			if errors.Is(err, storage.ErrConflict) {
				return ErrIdempotencyConflict
			}
			return err
		}
		turn = t
		created = wasCreated

		if !wasCreated {
			var cp turnCheckpoint
			_ = json.Unmarshal([]byte(t.CheckpointJSON), &cp)
			userMessageID = cp.UserMessageID
			switch t.Status {
			case domain.TurnStatusCompleted:
				return nil
			case domain.TurnStatusRunning:
				return nil
			default:
				// Resume failed/cancelled turn if room slot is free.
				sc, ok := e.hub.StartTurn(roomID, t.ID)
				if !ok {
					return ErrRoomBusy
				}
				specCtx = sc
				acquiredSlot = true
				turn.Status = domain.TurnStatusRunning
				turn.ErrorCode = ""
				turn.ErrorMessage = ""
				turn.CompletedAt = nil
				return tx.Turns().Update(ctx, turn)
			}
		}

		// New turn requires exclusive room turn slot.
		sc, ok := e.hub.StartTurn(roomID, turn.ID)
		if !ok {
			return ErrRoomBusy
		}
		specCtx = sc
		acquiredSlot = true

		observedBy := make([]string, 0, len(members)+1)
		if room.PersonaID != "" {
			observedBy = append(observedBy, room.PersonaID)
		}
		for _, m := range members {
			if m.Active {
				observedBy = append(observedBy, m.CharacterID)
			}
		}

		userMsg, err := e.commitMessageTx(ctx, tx, roomID, turn.ID, domain.MessageRoleUser, room.PersonaID, room.PersonaSnapshot.Name, req.Text, req.LocationTag, observedBy)
		if err != nil {
			return err
		}
		userMessageID = userMsg.ID
		actorID := ""
		if room.Mode == domain.RoomModeDirect {
			actorID = activeMember.CharacterID
		}
		cpBytes, _ := json.Marshal(turnCheckpoint{
			UserMessageID: userMessageID,
			ActorID:       actorID,
			Phase:         "submitted",
		})
		turn.CheckpointJSON = string(cpBytes)
		return tx.Turns().Update(ctx, turn)
	})
	e.mu.Unlock()

	if err != nil {
		if acquiredSlot {
			e.hub.FinishTurn(roomID, turn.ID)
		}
		return SubmitTurnResult{}, err
	}

	if !created && turn.Status == domain.TurnStatusCompleted {
		return SubmitTurnResult{
			Turn:             turn,
			UserMessageID:    userMessageID,
			AlreadyCompleted: true,
		}, nil
	}
	if !created && !acquiredSlot && turn.Status == domain.TurnStatusRunning {
		return SubmitTurnResult{
			Turn:           turn,
			UserMessageID:  userMessageID,
			AlreadyRunning: true,
		}, nil
	}

	if created {
		e.events.Publish(roomID, "turn.started", map[string]any{
			"turn_id":         turn.ID,
			"request_id":      turn.RequestID,
			"user_message_id": userMessageID,
		})
	}
	e.launchTurn(roomID, turn, specCtx)

	return SubmitTurnResult{
		Turn:          turn,
		UserMessageID: userMessageID,
	}, nil
}

// RetryTurn resumes a failed or cancelled turn from its saved checkpoint.
func (e *Engine) RetryTurn(ctx context.Context, roomID, turnID string) (domain.Turn, error) {
	if _, err := e.rdb.Rooms().Get(ctx, roomID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Turn{}, ErrRoomNotFound
		}
		return domain.Turn{}, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return domain.Turn{}, errors.New("runtime: engine is closed")
	}

	turn, err := e.rdb.Turns().Get(ctx, turnID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Turn{}, ErrTurnNotFound
		}
		return domain.Turn{}, err
	}
	if turn.RoomID != roomID {
		return domain.Turn{}, ErrTurnNotFound
	}
	if turn.Status != domain.TurnStatusFailed && turn.Status != domain.TurnStatusCancelled {
		return domain.Turn{}, fmt.Errorf("%w: status is %s", ErrTurnNotRetryable, turn.Status)
	}

	specCtx, ok := e.hub.StartTurn(roomID, turn.ID)
	if !ok {
		return domain.Turn{}, ErrRoomBusy
	}

	turn.Status = domain.TurnStatusRunning
	turn.ErrorCode = ""
	turn.ErrorMessage = ""
	turn.CompletedAt = nil
	if err := e.rdb.Turns().Update(ctx, turn); err != nil {
		e.hub.FinishTurn(roomID, turn.ID)
		return domain.Turn{}, err
	}

	var cp turnCheckpoint
	_ = json.Unmarshal([]byte(turn.CheckpointJSON), &cp)
	e.events.Publish(roomID, "turn.started", map[string]any{
		"turn_id":         turn.ID,
		"request_id":      turn.RequestID,
		"user_message_id": cp.UserMessageID,
	})
	e.launchTurn(roomID, turn, specCtx)
	return turn, nil
}

// Intercept triggers an immediate cut-in (non-empty text) or stop (empty text)
// on any active turn in roomID.
func (e *Engine) Intercept(roomID, text string) bool {
	turnID, kind, accepted := e.hub.Intercept(roomID, text)
	if !accepted {
		return false
	}
	payload := map[string]any{
		"turn_id": turnID,
		"kind":    kind,
	}
	if kind == "cut_in" {
		payload["text"] = strings.TrimSpace(text)
	}
	e.events.Publish(roomID, "turn.intercepted", payload)
	return true
}

type actorStepOutcome int

const (
	actorStepCommitted actorStepOutcome = iota
	actorStepPaused
	actorStepStopped
	actorStepCutIn
	actorStepFailed
)

type actorStepResult struct {
	outcome    actorStepOutcome
	nextCtx    context.Context
	replyText  string
	message    domain.Message
	observedBy []string
	usage      *llmprovider.Usage
	models     ResolvedModels
}

func latestUserMessageText(allMsgs []domain.Message) string {
	for i := len(allMsgs) - 1; i >= 0; i-- {
		if allMsgs[i].Role == domain.MessageRoleUser {
			return allMsgs[i].Content
		}
	}
	return ""
}

func (e *Engine) launchTurn(roomID string, turn domain.Turn, specCtx context.Context) {
	e.bgWG.Add(1)
	go func() {
		defer e.bgWG.Done()
		defer e.hub.FinishTurn(roomID, turn.ID)
		room, err := e.rdb.Rooms().Get(context.Background(), roomID)
		if err == nil && room.Mode == domain.RoomModeGroup {
			e.executeGroupTurn(roomID, turn, specCtx)
			return
		}
		e.executeDirectTurn(roomID, turn, specCtx)
	}()
}

func (e *Engine) executeDirectTurn(roomID string, turn domain.Turn, specCtx context.Context) {
	baseCtx := context.Background()
	var cp turnCheckpoint
	_ = json.Unmarshal([]byte(turn.CheckpointJSON), &cp)

	for {
		if e.hub.IsStopped(roomID, turn.ID) {
			e.finishCancelled(baseCtx, roomID, &turn)
			return
		}

		room, err := e.rdb.Rooms().Get(baseCtx, roomID)
		if err != nil {
			e.finishFailed(baseCtx, roomID, &turn, "internal", err.Error(), false)
			return
		}
		members, err := e.rdb.Rooms().ListMembers(baseCtx, roomID)
		if err != nil {
			e.finishFailed(baseCtx, roomID, &turn, "internal", err.Error(), true)
			return
		}
		var member *domain.RoomMember
		for i := range members {
			if members[i].Active {
				member = &members[i]
				break
			}
		}
		if member == nil {
			e.finishFailed(baseCtx, roomID, &turn, "invalid_input", "no active character in room", false)
			return
		}
		allMsgs, err := e.rdb.Messages().List(baseCtx, roomID, 0, 500)
		if err != nil {
			e.finishFailed(baseCtx, roomID, &turn, "internal", err.Error(), true)
			return
		}
		latestUserText := latestUserMessageText(allMsgs)

		res := e.executeActorStep(baseCtx, specCtx, roomID, &turn, &cp, room, members, member, latestUserText, true)
		switch res.outcome {
		case actorStepPaused, actorStepStopped, actorStepFailed:
			return
		case actorStepCutIn:
			specCtx = res.nextCtx
			cp = reloadCheckpoint(turn.CheckpointJSON)
			continue
		case actorStepCommitted:
			confirmed := fmt.Sprintf("%s: %s\n%s: %s", room.PersonaSnapshot.Name, latestUserText, member.CharacterSnapshot.Name, res.replyText)
			e.schedulePostCommit(room, *member, members, turn.ID, confirmed, res.observedBy, res.models)
			return
		}
	}
}

func (e *Engine) executeActorStep(
	baseCtx context.Context,
	specCtx context.Context,
	roomID string,
	turn *domain.Turn,
	cp *turnCheckpoint,
	room domain.Room,
	members []domain.RoomMember,
	member *domain.RoomMember,
	latestUserText string,
	completeTurn bool,
) actorStepResult {
	for {
		if e.hub.IsStopped(roomID, turn.ID) {
			e.finishCancelled(baseCtx, roomID, turn)
			return actorStepResult{outcome: actorStepStopped}
		}

		snap := e.providers.Snapshot()
		if snap.Gateway == nil {
			e.finishFailed(baseCtx, roomID, turn, "model_unavailable", "no LLM provider is enabled or initialized", false)
			return actorStepResult{outcome: actorStepFailed}
		}
		models := ResolveModels(baseCtx, snap.Config, room, member.CharacterSnapshot, snap.Gateway)
		if models.ChatModel == "" {
			e.finishFailed(baseCtx, roomID, turn, "model_unavailable", "no chat model configured for character, room, or defaults", false)
			return actorStepResult{outcome: actorStepFailed}
		}
		modelMeta, err := snap.Gateway.Model(baseCtx, models.ChatModel)
		if err != nil {
			e.finishFailed(baseCtx, roomID, turn, "model_unavailable", fmt.Sprintf("configured chat model %q is unavailable: %v", models.ChatModel, err), false)
			return actorStepResult{outcome: actorStepFailed}
		}

		allMsgs, err := e.rdb.Messages().List(baseCtx, roomID, 0, 500)
		if err != nil {
			e.finishFailed(baseCtx, roomID, turn, "internal", err.Error(), true)
			return actorStepResult{outcome: actorStepFailed}
		}
		latestUserText = latestUserMessageText(allMsgs)

		toolReg, dataDir := e.currentTools()

		// Step 0: Resume any pending approved/rejected/answered tool calls before starting a new LLM step.
		if len(cp.RemainingCalls) > 0 && toolReg != nil {
			outcome, nextCtx := e.processToolCalls(baseCtx, specCtx, roomID, turn, cp, room, members, *member, snap, models, latestUserText, toolReg, dataDir)
			switch outcome {
			case toolLoopPaused:
				return actorStepResult{outcome: actorStepPaused}
			case toolLoopStopped:
				return actorStepResult{outcome: actorStepStopped}
			case toolLoopFailed:
				return actorStepResult{outcome: actorStepFailed}
			case toolLoopCutIn:
				return actorStepResult{outcome: actorStepCutIn, nextCtx: nextCtx}
			}
		}

		// Step 1: Memory Recall (skip redundant recall inside multi-step tool loop).
		var recalled []memory.RecalledItem
		if len(cp.ToolHistory) == 0 {
			e.events.Publish(roomID, "actor.status", map[string]any{
				"turn_id":    turn.ID,
				"actor_id":   member.CharacterID,
				"actor_name": member.CharacterSnapshot.Name,
				"phase":      "recalling",
			})

			recentExcerpts, excludeIDs := buildRecentExcerpts(allMsgs, member.CharacterID, 8)
			var decEval memory.DecisionEvaluator
			if snap.Decisions != nil {
				decEval = snap.Decisions
			}
			recalled, _ = memory.Recall(specCtx, e.rdb, e.search, memory.Deps{
				Chat:      snap.Gateway,
				Models:    snap.Gateway,
				Decisions: decEval,
				Embedder:  snap.Embedder,
			}, memory.RecallInput{
				RoomID:             roomID,
				ActorID:            member.CharacterID,
				ActorName:          member.CharacterSnapshot.Name,
				UserName:           room.PersonaSnapshot.Name,
				CurrentText:        latestUserText,
				Recent:             recentExcerpts,
				ExcludeIDs:         excludeIDs,
				DecisionModel:      models.DecisionModel,
				RecallPlannerModel: models.RecallPlannerModel,
				OwnBudget:          models.ContextWindow,
				NowMS:              domain.NowMS(),
			})

			if interrupted, nextCtx := e.handlePostCancel(baseCtx, roomID, turn, room, members); interrupted == interceptStop {
				return actorStepResult{outcome: actorStepStopped}
			} else if interrupted == interceptCutIn {
				return actorStepResult{outcome: actorStepCutIn, nextCtx: nextCtx}
			}

			// Step 2: 85% budget compaction if needed
			updatedMember, compacted, compErr := CompactIfNeeded(specCtx, e.rdb, snap.Gateway, room, *member, recalled, allMsgs, models, turn.ID)
			if compErr == nil && compacted {
				*member = updatedMember
			}
			if interrupted, nextCtx := e.handlePostCancel(baseCtx, roomID, turn, room, members); interrupted == interceptStop {
				return actorStepResult{outcome: actorStepStopped}
			} else if interrupted == interceptCutIn {
				return actorStepResult{outcome: actorStepCutIn, nextCtx: nextCtx}
			}
		}

		// Step 3: Build Actor Prompt (+ accumulated ToolHistory for this turn)
		prompt, err := BuildActorPrompt(room, *member, recalled, allMsgs, models.ContextWindow)
		if err != nil {
			code := "internal"
			if errors.Is(err, memory.ErrValuesTooLarge) {
				code = "values_too_large"
			}
			e.finishFailed(baseCtx, roomID, turn, code, err.Error(), false)
			return actorStepResult{outcome: actorStepFailed}
		}
		reqMessages := make([]llmprovider.Message, 0, len(prompt.Messages)+len(cp.ToolHistory))
		reqMessages = append(reqMessages, prompt.Messages...)
		reqMessages = append(reqMessages, cp.ToolHistory...)

		var toolDefs []llmprovider.Tool
		if toolReg != nil && (modelMeta.Capabilities == nil || modelMeta.Capabilities.ToolCalling == nil || *modelMeta.Capabilities.ToolCalling) {
			toolDefs = toolReg.ToolDefinitions()
		}

		// Step 4: Stream response
		e.events.Publish(roomID, "actor.status", map[string]any{
			"turn_id":    turn.ID,
			"actor_id":   member.CharacterID,
			"actor_name": member.CharacterSnapshot.Name,
			"phase":      "thinking",
		})

		stream, err := snap.Gateway.ChatStream(specCtx, llmprovider.ChatRequest{
			Model:           models.ChatModel,
			Messages:        reqMessages,
			Tools:           toolDefs,
			ReasoningEffort: models.ReasoningEffort,
		})
		if err != nil {
			if interrupted, nextCtx := e.handlePostCancel(baseCtx, roomID, turn, room, members); interrupted == interceptStop {
				return actorStepResult{outcome: actorStepStopped}
			} else if interrupted == interceptCutIn {
				return actorStepResult{outcome: actorStepCutIn, nextCtx: nextCtx}
			}
			e.finishFailed(baseCtx, roomID, turn, "provider_error", err.Error(), true)
			return actorStepResult{outcome: actorStepFailed}
		}

		messageID := domain.NewID()
		var buf strings.Builder
		toolCallAcc := make(map[int]*llmprovider.ToolCall)
		var streamErr error
		var lastUsage *llmprovider.Usage
		for {
			chunk, recvErr := stream.Recv()
			if errors.Is(recvErr, io.EOF) {
				break
			}
			if recvErr != nil {
				streamErr = recvErr
				break
			}
			if chunk == nil {
				continue
			}
			if chunk.Usage != nil {
				u := *chunk.Usage
				lastUsage = &u
			}
			if len(chunk.Choices) == 0 {
				continue
			}
			ch := chunk.Choices[0]
			delta := ""
			if ch.Delta != nil {
				delta = ch.Delta.TextContent()
				if len(ch.Delta.ToolCalls) > 0 {
					accumulateToolCallChunk(toolCallAcc, ch.Delta.ToolCalls)
				}
			} else {
				delta = ch.Message.TextContent()
				if len(ch.Message.ToolCalls) > 0 {
					accumulateToolCallChunk(toolCallAcc, ch.Message.ToolCalls)
				}
			}
			if delta == "" {
				continue
			}
			buf.WriteString(delta)
			e.events.Publish(roomID, "bubble.delta", map[string]any{
				"turn_id":    turn.ID,
				"message_id": messageID,
				"actor_id":   member.CharacterID,
				"delta":      delta,
			})
		}
		_ = stream.Close()

		// Check intercept first before treating context cancellation as provider error.
		if interrupted, nextCtx := e.handlePostCancel(baseCtx, roomID, turn, room, members); interrupted == interceptStop {
			return actorStepResult{outcome: actorStepStopped}
		} else if interrupted == interceptCutIn {
			return actorStepResult{outcome: actorStepCutIn, nextCtx: nextCtx}
		}
		if streamErr != nil {
			e.finishFailed(baseCtx, roomID, turn, "provider_error", streamErr.Error(), true)
			return actorStepResult{outcome: actorStepFailed}
		}

		toolCalls := finalizeToolCalls(toolCallAcc)
		if len(toolCalls) > 0 && toolReg != nil {
			if cp.ToolRounds >= maxToolRounds {
				e.finishFailed(baseCtx, roomID, turn, "provider_error", "exceeded max tool rounds", false)
				return actorStepResult{outcome: actorStepFailed}
			}
			cp.ToolHistory = append(cp.ToolHistory, llmprovider.Message{
				Role:      llmprovider.RoleAssistant,
				Content:   buf.String(),
				ToolCalls: toolCalls,
			})
			cp.RemainingCalls = toolCalls
			cp.ToolRounds++
			cp.Phase = "tool_loop"
			cpBytes, _ := json.Marshal(cp)
			turn.CheckpointJSON = string(cpBytes)
			_ = e.rdb.Turns().Update(baseCtx, *turn)

			outcome, nextCtx := e.processToolCalls(baseCtx, specCtx, roomID, turn, cp, room, members, *member, snap, models, latestUserText, toolReg, dataDir)
			switch outcome {
			case toolLoopPaused:
				return actorStepResult{outcome: actorStepPaused}
			case toolLoopStopped:
				return actorStepResult{outcome: actorStepStopped}
			case toolLoopFailed:
				return actorStepResult{outcome: actorStepFailed}
			case toolLoopCutIn:
				return actorStepResult{outcome: actorStepCutIn, nextCtx: nextCtx}
			default:
				continue
			}
		}

		replyText := strings.TrimSpace(buf.String())
		if replyText == "" {
			e.finishFailed(baseCtx, roomID, turn, "provider_error", "model returned an empty response", true)
			return actorStepResult{outcome: actorStepFailed}
		}

		// Step 5: Atomic RDB commit of bubble + outbox (+ turn completion when completeTurn).
		observedBy := make([]string, 0, len(members)+1)
		if room.PersonaID != "" {
			observedBy = append(observedBy, room.PersonaID)
		}
		for _, m := range members {
			if m.Active {
				observedBy = append(observedBy, m.CharacterID)
			}
		}

		metaJSON := ""
		if lastUsage != nil {
			if b, err := json.Marshal(map[string]any{"usage": lastUsage}); err == nil {
				metaJSON = string(b)
			}
		}

		var committedMsg domain.Message
		nowMS := domain.NowMS()
		err = e.rdb.Tx(baseCtx, func(tx storage.RDBTx) error {
			msg, err := e.commitMessageWithMetaTx(baseCtx, tx, messageID, roomID, turn.ID, domain.MessageRoleCharacter, member.CharacterID, member.CharacterSnapshot.Name, replyText, "", metaJSON, observedBy)
			if err != nil {
				return err
			}
			committedMsg = msg
			if completeTurn {
				turn.Status = domain.TurnStatusCompleted
				turn.CompletedAt = &nowMS
				turn.ErrorCode = ""
				turn.ErrorMessage = ""
				return tx.Turns().Update(baseCtx, *turn)
			}
			return nil
		})
		if err != nil {
			e.finishFailed(baseCtx, roomID, turn, "internal", err.Error(), true)
			return actorStepResult{outcome: actorStepFailed}
		}

		e.events.Publish(roomID, "bubble.completed", map[string]any{
			"turn_id": turn.ID,
			"message": committedMsg,
		})
		if completeTurn {
			donePayload := map[string]any{
				"turn_id": turn.ID,
				"status":  string(domain.TurnStatusCompleted),
			}
			if lastUsage != nil {
				donePayload["usage"] = lastUsage
			}
			e.events.Publish(roomID, "turn.completed", donePayload)
		}

		return actorStepResult{
			outcome:    actorStepCommitted,
			replyText:  replyText,
			message:    committedMsg,
			observedBy: observedBy,
			usage:      lastUsage,
			models:     models,
		}
	}
}

type interceptOutcome int

const (
	interceptNone interceptOutcome = iota
	interceptStop
	interceptCutIn
)

func (e *Engine) handlePostCancel(ctx context.Context, roomID string, turn *domain.Turn, room domain.Room, members []domain.RoomMember) (interceptOutcome, context.Context) {
	if e.hub.IsStopped(roomID, turn.ID) {
		e.finishCancelled(ctx, roomID, turn)
		return interceptStop, nil
	}
	cutInText, ok := e.hub.TakeCutIn(roomID, turn.ID)
	if !ok {
		return interceptNone, nil
	}

	observedBy := make([]string, 0, len(members)+1)
	if room.PersonaID != "" {
		observedBy = append(observedBy, room.PersonaID)
	}
	for _, m := range members {
		if m.Active {
			observedBy = append(observedBy, m.CharacterID)
		}
	}

	var cutInMsg domain.Message
	err := e.rdb.Tx(ctx, func(tx storage.RDBTx) error {
		msg, err := e.commitMessageTx(ctx, tx, roomID, turn.ID, domain.MessageRoleUser, room.PersonaID, room.PersonaSnapshot.Name, cutInText, "", observedBy)
		if err != nil {
			return err
		}
		cutInMsg = msg
		cpBytes, _ := json.Marshal(turnCheckpoint{
			UserMessageID: cutInMsg.ID,
			Phase:         "cut_in",
		})
		turn.CheckpointJSON = string(cpBytes)
		return tx.Turns().Update(ctx, *turn)
	})
	if err != nil {
		e.finishFailed(ctx, roomID, turn, "internal", err.Error(), true)
		return interceptStop, nil
	}

	e.events.Publish(roomID, "bubble.completed", map[string]any{
		"turn_id": turn.ID,
		"message": cutInMsg,
	})

	nextCtx, restarted := e.hub.RestartSpeculative(roomID, turn.ID)
	if !restarted {
		e.finishCancelled(ctx, roomID, turn)
		return interceptStop, nil
	}
	return interceptCutIn, nextCtx
}

func (e *Engine) commitMessageTx(
	ctx context.Context,
	tx storage.RDBTx,
	roomID, turnID string,
	role domain.MessageRole,
	authorID, authorName, content, locationTag string,
	observedBy []string,
) (domain.Message, error) {
	return e.commitMessageWithMetaTx(ctx, tx, "", roomID, turnID, role, authorID, authorName, content, locationTag, "", observedBy)
}

func (e *Engine) commitMessageWithMetaTx(
	ctx context.Context,
	tx storage.RDBTx,
	id, roomID, turnID string,
	role domain.MessageRole,
	authorID, authorName, content, locationTag, metadataJSON string,
	observedBy []string,
) (domain.Message, error) {
	if id == "" {
		id = domain.NewID()
	}
	msg, err := tx.Messages().Append(ctx, domain.Message{
		ID:           id,
		RoomID:       roomID,
		TurnID:       turnID,
		Role:         role,
		AuthorID:     authorID,
		AuthorName:   authorName,
		Content:      content,
		ObservedBy:   observedBy,
		LocationTag:  locationTag,
		MetadataJSON: metadataJSON,
		CreatedAt:    domain.NowMS(),
	})
	if err != nil {
		return domain.Message{}, err
	}
	payloadBytes, _ := json.Marshal(outbox.Payload{
		Scope:      "room:" + roomID,
		OwnerID:    authorID,
		ObservedBy: observedBy,
		Text:       content,
		OccurredAt: msg.CreatedAt,
	})
	if _, err := tx.Outbox().Enqueue(ctx, domain.OutboxEntry{
		EntityType:  "message",
		EntityID:    msg.ID,
		Op:          domain.OutboxOpUpsert,
		Revision:    msg.Seq,
		PayloadJSON: string(payloadBytes),
		Status:      domain.OutboxStatusPending,
	}); err != nil {
		return domain.Message{}, err
	}
	return msg, nil
}

func (e *Engine) schedulePostCommit(
	room domain.Room,
	member domain.RoomMember,
	members []domain.RoomMember,
	turnID, confirmedText string,
	observedBy []string,
	models ResolvedModels,
) {
	e.bgWG.Add(1)
	go func() {
		defer e.bgWG.Done()
		ctx := context.Background()
		snap := e.providers.Snapshot()

		identity := "Character: " + member.CharacterSnapshot.Name
		if member.CharacterSnapshot.Personality != "" {
			identity += "\nPersonality: " + member.CharacterSnapshot.Personality
		}

		if snap.Gateway != nil && models.MemoryModel != "" {
			if _, err := memory.ExtractAndStoreMemories(ctx, e.rdb, snap.Gateway, memory.ExtractMemoriesParams{
				RoomID:          room.ID,
				ActorID:         member.CharacterID,
				ActorName:       member.CharacterSnapshot.Name,
				TurnID:          turnID,
				Identity:        identity,
				MemoryModel:     models.MemoryModel,
				ReasoningEffort: models.MemoryReasoningEffort,
				ConfirmedText:   confirmedText,
				ObservedBy:      []string{member.CharacterID},
				NowMS:           domain.NowMS(),
			}); err != nil {
				e.logger.Warn("post-commit memory extraction failed", "turn_id", turnID, "error", err)
			}
		}

		if snap.Decisions != nil && models.DecisionModel != "" {
			targetIDs := make([]string, 0, len(members)+1)
			if room.PersonaID != "" {
				targetIDs = append(targetIDs, room.PersonaID)
			}
			for _, m := range members {
				if m.Active && m.CharacterID != member.CharacterID {
					targetIDs = append(targetIDs, m.CharacterID)
				}
			}
			nextState, changed, err := memory.AppraiseState(ctx, snap.Decisions, memory.AppraiseStateParams{
				ActorName:     member.CharacterSnapshot.Name,
				DecisionModel: models.DecisionModel,
				CurrentState:  member.ActorState,
				TargetIDs:     targetIDs,
				ConfirmedText: confirmedText,
				NowMS:         domain.NowMS(),
			})
			if err != nil {
				e.logger.Warn("post-commit state appraisal failed", "turn_id", turnID, "error", err)
			} else if changed {
				_ = e.rdb.Tx(ctx, func(tx storage.RDBTx) error {
					members, err := tx.Rooms().ListMembers(ctx, room.ID)
					if err != nil {
						return err
					}
					for _, cur := range members {
						if cur.CharacterID == member.CharacterID {
							cur.ActorState.Self = nextState.Self
							cur.ActorState.StateUpdatedAt = nextState.StateUpdatedAt
							cur.ActorState.Relationships = nextState.Relationships
							cur.ActorState.RelationshipsUpdatedAt = nextState.RelationshipsUpdatedAt
							return tx.Rooms().UpsertMember(ctx, cur)
						}
					}
					return nil
				})
			}
		}
		_ = observedBy
	}()
}

func (e *Engine) finishCancelled(ctx context.Context, roomID string, turn *domain.Turn) {
	nowMS := domain.NowMS()
	turn.Status = domain.TurnStatusCancelled
	turn.CompletedAt = &nowMS
	_ = e.rdb.Turns().Update(ctx, *turn)
	e.events.Publish(roomID, "turn.completed", map[string]any{
		"turn_id": turn.ID,
		"status":  string(domain.TurnStatusCancelled),
	})
}

func (e *Engine) finishFailed(ctx context.Context, roomID string, turn *domain.Turn, code, msg string, retryable bool) {
	nowMS := domain.NowMS()
	turn.Status = domain.TurnStatusFailed
	turn.ErrorCode = code
	turn.ErrorMessage = msg
	turn.CompletedAt = &nowMS
	_ = e.rdb.Turns().Update(ctx, *turn)
	e.events.Publish(roomID, "turn.failed", map[string]any{
		"turn_id": turn.ID,
		"error": map[string]any{
			"code":      code,
			"message":   msg,
			"retryable": retryable,
		},
	})
}
