package runtime

import (
	"context"
	"encoding/json"
	"math"
	"strings"

	sdk "github.com/snowmerak/decisions"
	"github.com/snowmerak/q/v2/pkg/domain"
	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
	"github.com/snowmerak/q/v2/pkg/llm/decisions"
	"github.com/snowmerak/q/v2/pkg/memory"
)

const defaultGroupMaxTurns = 3

// EffectiveSpeed computes a group participant's current turn-acquisition speed
// from its base speed, affect state, consecutive-turn damping, and name mentions.
func EffectiveSpeed(member domain.RoomMember, messages []domain.Message) float64 {
	base := member.BaseSpeed
	if base <= 0 {
		base = 50
	}
	speed := float64(base) / 50.0

	if member.ActorState.Self.Stressed {
		speed *= 0.85
	}
	if member.ActorState.Self.Activation > 40 {
		speed *= 1.15
	} else if member.ActorState.Self.Activation < -40 {
		speed *= 0.85
	}

	var latestVisible *domain.Message
	selfStreak := 0
	countingStreak := true
	for i := len(messages) - 1; i >= 0; i-- {
		m := &messages[i]
		if m.Role == domain.MessageRoleSystem || !m.ObservedByActor(member.CharacterID) {
			continue
		}
		if latestVisible == nil {
			latestVisible = m
		}
		if !countingStreak {
			continue
		}
		if m.Role == domain.MessageRoleUser {
			countingStreak = false
			continue
		}
		if m.AuthorID == member.CharacterID {
			selfStreak++
		} else {
			countingStreak = false
		}
	}

	switch {
	case selfStreak == 1:
		speed *= 0.50
	case selfStreak >= 2:
		speed *= 0.20
	}

	if latestVisible != nil && latestVisible.AuthorID != member.CharacterID {
		name := strings.TrimSpace(member.CharacterSnapshot.Name)
		if name != "" && strings.Contains(strings.ToLower(latestVisible.Content), strings.ToLower(name)) {
			speed *= 1.40
		}
	}

	if speed < 0.1 {
		return 0.1
	}
	return speed
}

// ReserveActor advances all eligible active members' pace gauges until at least
// one reaches 1.0, selects the highest-gauge actor, and consumes 1.0 from its gauge.
func ReserveActor(
	members []domain.RoomMember,
	gauges map[string]float64,
	waited []string,
	messages []domain.Message,
) (*domain.RoomMember, map[string]float64) {
	nextGauges := make(map[string]float64, len(members))
	for k, v := range gauges {
		nextGauges[k] = v
	}

	waitedSet := make(map[string]struct{}, len(waited))
	for _, id := range waited {
		waitedSet[id] = struct{}{}
	}

	eligible := make([]domain.RoomMember, 0, len(members))
	speeds := make(map[string]float64, len(members))
	minDT := math.Inf(1)
	for _, m := range members {
		if !m.Active {
			continue
		}
		if _, skip := waitedSet[m.CharacterID]; skip {
			continue
		}
		spd := EffectiveSpeed(m, messages)
		speeds[m.CharacterID] = spd
		g := nextGauges[m.CharacterID]
		dt := 0.0
		if g < 1.0 {
			dt = (1.0 - g) / spd
		}
		if dt < minDT {
			minDT = dt
		}
		eligible = append(eligible, m)
	}
	if len(eligible) == 0 {
		return nil, nextGauges
	}

	selectedIdx := 0
	bestGauge := math.Inf(-1)
	for i, m := range eligible {
		g := nextGauges[m.CharacterID] + minDT*speeds[m.CharacterID]
		nextGauges[m.CharacterID] = g
		if g > bestGauge {
			bestGauge = g
			selectedIdx = i
		}
	}

	chosen := eligible[selectedIdx]
	nextGauges[chosen.CharacterID] = math.Max(0, nextGauges[chosen.CharacterID]-1.0)
	return &chosen, nextGauges
}

func recentVisibleExcerpts(messages []domain.Message, actorID string, limit int) []memory.RecentExcerpt {
	out := make([]memory.RecentExcerpt, 0, limit)
	for i := len(messages) - 1; i >= 0 && len(out) < limit; i-- {
		m := messages[i]
		if m.Role == domain.MessageRoleSystem {
			continue
		}
		if actorID != "" && !m.ObservedByActor(actorID) {
			continue
		}
		out = append(out, memory.RecentExcerpt{
			AuthorName: m.AuthorName,
			Content:    m.Content,
			CreatedAt:  m.CreatedAt,
		})
	}
	for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
		out[l], out[r] = out[r], out[l]
	}
	return out
}

func (e *Engine) evaluateParticipation(
	ctx context.Context,
	dec memory.DecisionEvaluator,
	modelID string,
	room domain.Room,
	member domain.RoomMember,
	messages []domain.Message,
) bool {
	if dec == nil || strings.TrimSpace(modelID) == "" {
		return true
	}
	resp, err := dec.Evaluate(ctx, modelID, sdk.Input{
		State: map[string]any{
			"room_title":  room.Title,
			"actor_id":    member.CharacterID,
			"actor_name":  member.CharacterSnapshot.Name,
			"personality": member.CharacterSnapshot.Personality,
			"user_name":   room.PersonaSnapshot.Name,
			"recent":      recentVisibleExcerpts(messages, member.CharacterID, 8),
		},
	}, sdk.Questions{
		"participation": sdk.ChoicesQuestion{
			Instructions: "Decide whether " + member.CharacterSnapshot.Name + " should speak or act right now in this group chat, or wait and yield the floor to others.",
			Choices: []sdk.Choice{
				{Value: "act", Description: "Speak or perform an action in response to the conversation."},
				{Value: "wait", Description: "Stay quiet or let another character respond first."},
			},
		},
	})
	if err != nil {
		if ctx.Err() == nil {
			e.logger.Warn("group participation check failed; defaulting to act", "room_id", room.ID, "actor_id", member.CharacterID, "error", err)
		}
		return true
	}
	if resp == nil {
		return true
	}
	ans, ok := resp.Answers["participation"].(sdk.ChoiceAnswer)
	if !ok {
		return true
	}
	return ans.Choice != "wait"
}

func (e *Engine) evaluateUserTurn(
	ctx context.Context,
	dec memory.DecisionEvaluator,
	modelID string,
	room domain.Room,
	activeCount int,
	messages []domain.Message,
	lastActorName string,
	lastReply string,
	stepsExecuted int,
) bool {
	if dec == nil || strings.TrimSpace(modelID) == "" {
		return stepsExecuted >= activeCount
	}
	resp, err := dec.Evaluate(ctx, modelID, sdk.Input{
		State: map[string]any{
			"room_title":      room.Title,
			"user_name":       room.PersonaSnapshot.Name,
			"last_actor_name": lastActorName,
			"last_reply":      lastReply,
			"steps_executed":  stepsExecuted,
			"active_members":  activeCount,
			"recent":          recentVisibleExcerpts(messages, "", 8),
		},
	}, sdk.Questions{
		"handoff": sdk.NoulQuestion{
			Instructions: "Decide whether control should now hand off back to the user so they can reply, rather than having another character speak immediately.",
		},
	})
	if err != nil {
		if ctx.Err() == nil {
			e.logger.Warn("group user_turn check failed; handing off to user", "room_id", room.ID, "error", err)
		}
		return true
	}
	positive, err := decisions.Positive(resp, "handoff")
	if err != nil {
		return true
	}
	return positive
}

func (e *Engine) saveCheckpoint(ctx context.Context, turn *domain.Turn, cp turnCheckpoint) {
	cpBytes, _ := json.Marshal(cp)
	turn.CheckpointJSON = string(cpBytes)
	_ = e.rdb.Turns().Update(ctx, *turn)
}

func (e *Engine) finishCompleted(ctx context.Context, roomID string, turn *domain.Turn, usage *llmprovider.Usage) error {
	nowMS := domain.NowMS()
	turn.Status = domain.TurnStatusCompleted
	turn.CompletedAt = &nowMS
	turn.ErrorCode = ""
	turn.ErrorMessage = ""
	if err := e.rdb.Turns().Update(ctx, *turn); err != nil {
		return err
	}
	donePayload := map[string]any{
		"turn_id": turn.ID,
		"status":  string(domain.TurnStatusCompleted),
	}
	if usage != nil {
		donePayload["usage"] = usage
	}
	e.hub.FinishTurn(roomID, turn.ID)
	e.events.Publish(roomID, "turn.completed", donePayload)
	return nil
}

func (e *Engine) executeGroupTurn(roomID string, turn domain.Turn, specCtx context.Context) {
	baseCtx := context.Background()
	var cp turnCheckpoint
	_ = json.Unmarshal([]byte(turn.CheckpointJSON), &cp)
	var lastUsage *llmprovider.Usage

	for {
		shouldContinue := func() bool {
			if e.hub.IsStopped(roomID, turn.ID) {
				e.finishCancelled(baseCtx, roomID, &turn)
				return false
			}

			room, err := e.rdb.Rooms().Get(baseCtx, roomID)
			if err != nil {
				e.finishFailed(baseCtx, roomID, &turn, "internal", err.Error(), false)
				return false
			}
			members, err := e.rdb.Rooms().ListMembers(baseCtx, roomID)
			if err != nil {
				e.finishFailed(baseCtx, roomID, &turn, "internal", err.Error(), true)
				return false
			}
			activeMembers := make([]domain.RoomMember, 0, len(members))
			for _, m := range members {
				if m.Active {
					activeMembers = append(activeMembers, m)
				}
			}
			if len(activeMembers) == 0 {
				e.finishFailed(baseCtx, roomID, &turn, "invalid_input", "no active character in room", false)
				return false
			}

			maxTurns := room.MaxTurnsPerInput
			if maxTurns <= 0 {
				maxTurns = defaultGroupMaxTurns
			}

			allMsgs, err := e.rdb.Messages().List(baseCtx, roomID, 0, 500)
			if err != nil {
				e.finishFailed(baseCtx, roomID, &turn, "internal", err.Error(), true)
				return false
			}
			latestUserText := latestUserMessageText(allMsgs)
			snap := e.providers.Snapshot()
			defer snap.Close()

			var member *domain.RoomMember
			resumingTools := len(cp.RemainingCalls) > 0 && cp.ActorID != ""
			if resumingTools {
				for i := range activeMembers {
					if activeMembers[i].CharacterID == cp.ActorID {
						m := activeMembers[i]
						member = &m
						break
					}
				}
			}
			if member == nil {
				if cp.GroupSteps >= maxTurns {
					if err := e.finishCompleted(baseCtx, roomID, &turn, lastUsage); err != nil {
						e.finishFailed(baseCtx, roomID, &turn, "internal", err.Error(), true)
					}
					return false
				}
				chosen, nextGauges := ReserveActor(activeMembers, cp.GroupGauges, cp.GroupWaited, allMsgs)
				if chosen == nil {
					if err := e.finishCompleted(baseCtx, roomID, &turn, lastUsage); err != nil {
						e.finishFailed(baseCtx, roomID, &turn, "internal", err.Error(), true)
					}
					return false
				}
				member = chosen
				cp.ActorID = chosen.CharacterID
				cp.GroupGauges = nextGauges
				cp.Phase = "group_reserved"
				e.saveCheckpoint(baseCtx, &turn, cp)

				if snap.Gateway == nil {
					e.finishFailed(baseCtx, roomID, &turn, "model_unavailable", "no LLM provider is enabled or initialized", false)
					return false
				}
				models := ResolveModels(baseCtx, snap.Config, room, member.CharacterSnapshot, snap.Gateway)
				var decEval memory.DecisionEvaluator
				if snap.Decisions != nil {
					decEval = snap.Decisions
				}
				if !e.evaluateParticipation(specCtx, decEval, models.DecisionModel, room, *member, allMsgs) {
					if interrupted, nextCtx := e.handlePostCancel(baseCtx, roomID, &turn, room, members); interrupted == interceptStop {
						return false
					} else if interrupted == interceptCutIn {
						specCtx = nextCtx
						cp = reloadCheckpoint(turn.CheckpointJSON)
						return true
					}
					cp.GroupWaited = append(cp.GroupWaited, member.CharacterID)
					cp.ActorID = ""
					e.saveCheckpoint(baseCtx, &turn, cp)
					return true
				}
				if interrupted, nextCtx := e.handlePostCancel(baseCtx, roomID, &turn, room, members); interrupted == interceptStop {
					return false
				} else if interrupted == interceptCutIn {
					specCtx = nextCtx
					cp = reloadCheckpoint(turn.CheckpointJSON)
					return true
				}
			}

			res := e.executeActorStep(baseCtx, specCtx, roomID, &turn, &cp, room, members, member, latestUserText, false)
			switch res.outcome {
			case actorStepPaused, actorStepStopped, actorStepFailed:
				return false
			case actorStepCutIn:
				specCtx = res.nextCtx
				cp = reloadCheckpoint(turn.CheckpointJSON)
				return true
			case actorStepCommitted:
				if res.usage != nil {
					lastUsage = res.usage
				}
				confirmed := fmtGroupConfirmed(room, member.CharacterSnapshot.Name, latestUserText, res.replyText, allMsgs)
				e.schedulePostCommit(room, *member, members, turn.ID, confirmed, res.observedBy, res.models)

				// Reset per-actor tool state after bubble commits; reset waited list since conversation advanced.
				cp.ToolHistory = nil
				cp.RemainingCalls = nil
				cp.PendingInvocationID = ""
				cp.PendingQuestionID = ""
				cp.PendingToolCallID = ""
				cp.ToolRounds = 0
				cp.Thinking = ""
				cp.GroupWaited = nil
				cp.ActorID = ""
				cp.GroupSteps++
				cp.Phase = "group_step_committed"
				e.saveCheckpoint(baseCtx, &turn, cp)

				if interrupted, nextCtx := e.handlePostCancel(baseCtx, roomID, &turn, room, members); interrupted == interceptStop {
					return false
				} else if interrupted == interceptCutIn {
					specCtx = nextCtx
					cp = reloadCheckpoint(turn.CheckpointJSON)
					return true
				}

				if cp.GroupSteps >= maxTurns {
					if err := e.finishCompleted(baseCtx, roomID, &turn, lastUsage); err != nil {
						e.finishFailed(baseCtx, roomID, &turn, "internal", err.Error(), true)
					}
					return false
				}

				updatedMsgs, _ := e.rdb.Messages().List(baseCtx, roomID, 0, 500)
				var decEval memory.DecisionEvaluator
				if snap.Decisions != nil {
					decEval = snap.Decisions
				}
				handoff := e.evaluateUserTurn(specCtx, decEval, res.models.DecisionModel, room, len(activeMembers), updatedMsgs, member.CharacterSnapshot.Name, res.replyText, cp.GroupSteps)
				if interrupted, nextCtx := e.handlePostCancel(baseCtx, roomID, &turn, room, members); interrupted == interceptStop {
					return false
				} else if interrupted == interceptCutIn {
					specCtx = nextCtx
					cp = reloadCheckpoint(turn.CheckpointJSON)
					return true
				}
				if handoff {
					if err := e.finishCompleted(baseCtx, roomID, &turn, lastUsage); err != nil {
						e.finishFailed(baseCtx, roomID, &turn, "internal", err.Error(), true)
					}
					return false
				}
			}
			return true
		}()
		if !shouldContinue {
			return
		}
	}
}

func reloadCheckpoint(raw string) turnCheckpoint {
	var cp turnCheckpoint
	_ = json.Unmarshal([]byte(raw), &cp)
	return cp
}

func fmtGroupConfirmed(room domain.Room, actorName, latestUserText, replyText string, prior []domain.Message) string {
	var b strings.Builder
	if len(prior) > 0 {
		last := prior[len(prior)-1]
		if last.AuthorName != "" && last.Content != "" {
			b.WriteString(last.AuthorName)
			b.WriteString(": ")
			b.WriteString(last.Content)
			b.WriteByte('\n')
		}
	} else if latestUserText != "" {
		b.WriteString(room.PersonaSnapshot.Name)
		b.WriteString(": ")
		b.WriteString(latestUserText)
		b.WriteByte('\n')
	}
	b.WriteString(actorName)
	b.WriteString(": ")
	b.WriteString(replyText)
	return b.String()
}
