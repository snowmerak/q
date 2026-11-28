package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/snowmerak/q/v2/pkg/app/config"
	"github.com/snowmerak/q/v2/pkg/domain"
	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
	"github.com/snowmerak/q/v2/pkg/memory"
	"github.com/snowmerak/q/v2/pkg/storage"
)

const (
	defaultContextWindow    = 32768
	compactionThresholdRate = 0.85
	compactionTargetRate    = 0.10
)

// ResolvedModels stores effective model IDs and context window resolved via:
// Room overrides > Character preferences > Global config defaults.
type ResolvedModels struct {
	ChatModel             string
	MemoryModel           string
	RecallPlannerModel    string
	DecisionModel         string
	ReasoningEffort       string
	MemoryReasoningEffort string
	ContextWindow         int
}

// ResolveModels computes effective model settings without silent model fallback
// when an explicit model is unconfigured.
func ResolveModels(ctx context.Context, cfg config.Config, room domain.Room, char domain.Character, lookup memory.ModelLookup) ResolvedModels {
	res := ResolvedModels{
		ChatModel:          strings.TrimSpace(cfg.LLM.Roles.ChatModel),
		MemoryModel:        strings.TrimSpace(cfg.LLM.Roles.MemoryModel),
		RecallPlannerModel: strings.TrimSpace(cfg.LLM.Roles.RecallPlannerModel),
		DecisionModel:      strings.TrimSpace(cfg.Decisions.DefaultModelID),
	}

	applyPrefs := func(p *domain.ModelPrefs) {
		if p == nil {
			return
		}
		if v := strings.TrimSpace(p.ChatModel); v != "" {
			res.ChatModel = v
		}
		if v := strings.TrimSpace(p.MemoryModel); v != "" {
			res.MemoryModel = v
		}
		if v := strings.TrimSpace(p.RecallPlannerModel); v != "" {
			res.RecallPlannerModel = v
		}
		if v := strings.TrimSpace(p.DecisionModel); v != "" {
			res.DecisionModel = v
		}
		if v := strings.TrimSpace(p.ReasoningEffort); v != "" {
			res.ReasoningEffort = v
		}
		if v := strings.TrimSpace(p.MemoryReasoningEffort); v != "" {
			res.MemoryReasoningEffort = v
		}
		if p.ContextWindowOverride > 0 {
			res.ContextWindow = p.ContextWindowOverride
		}
	}

	applyPrefs(char.ModelPrefs)
	applyPrefs(room.ModelOverrides)

	if res.MemoryModel == "" {
		res.MemoryModel = res.ChatModel
	}
	if res.RecallPlannerModel == "" {
		res.RecallPlannerModel = res.MemoryModel
	}
	if res.ContextWindow <= 0 && lookup != nil && res.ChatModel != "" {
		if meta, err := lookup.Model(ctx, res.ChatModel); err == nil && meta.ContextLength > 0 {
			res.ContextWindow = int(meta.ContextLength)
		}
	}
	if res.ContextWindow <= 0 {
		res.ContextWindow = defaultContextWindow
	}
	return res
}

// AssembledPrompt holds the ordered chat messages for an actor turn and its
// estimated input token count (excluding maxOutputTokens).
type AssembledPrompt struct {
	Messages             []llmprovider.Message
	ActiveMessages       []domain.Message
	EstimatedInputTokens int
}

// BuildActorPrompt assembles an append-only prefix prompt obeying observedBy
// isolation and chat-bubble presentation (no 3rd-person prose rewriting).
func BuildActorPrompt(
	room domain.Room,
	member domain.RoomMember,
	recalled []memory.RecalledItem,
	allMessages []domain.Message,
	contextWindow int,
) (AssembledPrompt, error) {
	if err := memory.AssertValuesBudget(member.ActorState.Values, contextWindow, 0.20); err != nil {
		return AssembledPrompt{}, err
	}

	char := member.CharacterSnapshot
	var stablePrefix strings.Builder
	stablePrefix.WriteString("You are roleplaying as ")
	stablePrefix.WriteString(char.Name)
	stablePrefix.WriteString(" in a real-time chat conversation.\n")
	stablePrefix.WriteString("Respond directly as this character in chat bubble form (first-person dialogue or natural conversational replies). Do NOT rewrite user actions or narrate in third-person novel prose.\n")
	if char.Personality != "" {
		stablePrefix.WriteString("\nPersonality:\n")
		stablePrefix.WriteString(char.Personality)
		stablePrefix.WriteByte('\n')
	}
	if char.Background != "" {
		stablePrefix.WriteString("\nBackground:\n")
		stablePrefix.WriteString(char.Background)
		stablePrefix.WriteByte('\n')
	}
	if char.Appearance != "" {
		stablePrefix.WriteString("\nAppearance:\n")
		stablePrefix.WriteString(char.Appearance)
		stablePrefix.WriteByte('\n')
	}
	if room.PersonaSnapshot.Name != "" {
		stablePrefix.WriteString("\nConversation Partner (User): ")
		stablePrefix.WriteString(room.PersonaSnapshot.Name)
		if room.PersonaSnapshot.Description != "" {
			stablePrefix.WriteString(" — ")
			stablePrefix.WriteString(room.PersonaSnapshot.Description)
		}
		stablePrefix.WriteByte('\n')
	}
	if vals := memory.RenderValues(member.ActorState.Values); vals != "" {
		stablePrefix.WriteByte('\n')
		stablePrefix.WriteString(vals)
	}
	if memo := strings.TrimSpace(member.ActorState.CompressedMemo); memo != "" {
		stablePrefix.WriteString("\nEarlier Conversation Summary:\n")
		stablePrefix.WriteString(memo)
		stablePrefix.WriteByte('\n')
	}

	msgs := make([]llmprovider.Message, 0, len(allMessages)+4)
	msgs = append(msgs, llmprovider.Message{
		Role:    llmprovider.RoleSystem,
		Content: strings.TrimSpace(stablePrefix.String()),
	})

	// Dynamic state + recalled memories follow the cacheable stable prefix.
	var dynamicCtx strings.Builder
	dynamicCtx.WriteString("Internal Affect State: valence=")
	dynamicCtx.WriteString(strconv.Itoa(member.ActorState.Self.Valence))
	dynamicCtx.WriteString(", activation=")
	dynamicCtx.WriteString(strconv.Itoa(member.ActorState.Self.Activation))
	dynamicCtx.WriteString(", stressed=")
	dynamicCtx.WriteString(strconv.FormatBool(member.ActorState.Self.Stressed))
	dynamicCtx.WriteByte('\n')
	if rel, ok := member.ActorState.Relationships[room.PersonaID]; ok {
		dynamicCtx.WriteString("Relationship with ")
		dynamicCtx.WriteString(room.PersonaSnapshot.Name)
		dynamicCtx.WriteString(": valence=")
		dynamicCtx.WriteString(strconv.Itoa(rel.Valence))
		dynamicCtx.WriteString(", activation=")
		dynamicCtx.WriteString(strconv.Itoa(rel.Activation))
		dynamicCtx.WriteString(", trust=")
		dynamicCtx.WriteString(strconv.Itoa(rel.Trust))
		dynamicCtx.WriteString(", stressed=")
		dynamicCtx.WriteString(strconv.FormatBool(rel.Stressed))
		dynamicCtx.WriteByte('\n')
	}
	if len(recalled) > 0 {
		dynamicCtx.WriteString("\nRecalled Memories & Earlier Dialogue:\n")
		for _, item := range recalled {
			ts := time.UnixMilli(item.OccurredAt).UTC().Format("2006-01-02 15:04")
			if item.Kind == "speech" && item.AuthorName != "" {
				dynamicCtx.WriteString(fmt.Sprintf("- [%s | %s] %s\n", ts, item.AuthorName, item.Text))
			} else {
				dynamicCtx.WriteString(fmt.Sprintf("- [%s] %s\n", ts, item.Text))
			}
		}
	}
	msgs = append(msgs, llmprovider.Message{
		Role:    llmprovider.RoleSystem,
		Content: strings.TrimSpace(dynamicCtx.String()),
	})

	visible := make([]domain.Message, 0, len(allMessages))
	for _, m := range allMessages {
		if m.Seq <= member.ActorState.CompactedThroughSeq {
			continue
		}
		if !m.ObservedByActor(member.CharacterID) {
			continue
		}
		visible = append(visible, m)
		role := llmprovider.RoleUser
		if m.Role == domain.MessageRoleCharacter && m.AuthorID == member.CharacterID {
			role = llmprovider.RoleAssistant
		} else if m.Role == domain.MessageRoleSystem {
			role = llmprovider.RoleSystem
		}
		content := m.Content
		if role == llmprovider.RoleUser && room.Mode == domain.RoomModeGroup && m.AuthorName != "" {
			content = m.AuthorName + ": " + content
		}
		if m.LocationTag != "" && role == llmprovider.RoleUser {
			content = "[" + m.LocationTag + "] " + content
		}
		msgs = append(msgs, llmprovider.Message{
			Role:    role,
			Content: content,
		})
	}

	est := 0
	for _, m := range msgs {
		est += memory.EstimateTokens(m.Content) + 6
	}

	return AssembledPrompt{
		Messages:             msgs,
		ActiveMessages:       visible,
		EstimatedInputTokens: est,
	}, nil
}

// CompactIfNeeded summarizes older visible messages into CompressedMemo when
// estimated input tokens exceed 85% of ContextWindow, keeping ~10% raw tail
// and queueing extracted value candidates for background consolidation.
func CompactIfNeeded(
	ctx context.Context,
	rdb storage.RDBStore,
	chat memory.ChatCompleter,
	room domain.Room,
	member domain.RoomMember,
	recalled []memory.RecalledItem,
	allMessages []domain.Message,
	models ResolvedModels,
	turnID string,
) (domain.RoomMember, bool, error) {
	assembled, err := BuildActorPrompt(room, member, recalled, allMessages, models.ContextWindow)
	if err != nil {
		return member, false, err
	}
	threshold := int(float64(models.ContextWindow) * compactionThresholdRate)
	if assembled.EstimatedInputTokens <= threshold || len(assembled.ActiveMessages) < 2 || chat == nil || models.MemoryModel == "" {
		return member, false, nil
	}

	keepBudget := max(128, int(float64(models.ContextWindow)*compactionTargetRate))
	splitIdx := len(assembled.ActiveMessages) - 1
	keptTokens := memory.EstimateTokens(assembled.ActiveMessages[splitIdx].Content) + 8
	for splitIdx > 0 {
		nextCost := memory.EstimateTokens(assembled.ActiveMessages[splitIdx-1].Content) + 8
		if keptTokens+nextCost > keepBudget {
			break
		}
		splitIdx--
		keptTokens += nextCost
	}
	if splitIdx == 0 {
		// Cannot split away at least one older message while preserving the latest message.
		return member, false, nil
	}

	older := assembled.ActiveMessages[:splitIdx]
	var segment strings.Builder
	for _, m := range older {
		segment.WriteString(m.AuthorName)
		segment.WriteString(": ")
		segment.WriteString(m.Content)
		segment.WriteByte('\n')
	}
	segmentText := strings.TrimSpace(segment.String())

	// Step 1: Extract value candidates before losing raw detail.
	identity := "Character: " + member.CharacterSnapshot.Name
	if member.CharacterSnapshot.Personality != "" {
		identity += "\nPersonality: " + member.CharacterSnapshot.Personality
	}
	candidates, _ := memory.ExtractValueCandidates(ctx, chat, memory.ExtractCandidatesParams{
		Identity:        identity,
		MemoryModel:     models.MemoryModel,
		ReasoningEffort: models.MemoryReasoningEffort,
		ExistingValues:  member.ActorState.Values,
		CompressedMemo:  member.ActorState.CompressedMemo,
		SegmentText:     segmentText,
		MaxTokens:       keepBudget,
	})

	// Step 2: Summarize existing memo + older segment targeting ~10% of ContextWindow.
	summaryPayload, _ := json.Marshal(map[string]any{
		"previous_memo": member.ActorState.CompressedMemo,
		"segment":       segmentText,
		"target_tokens": keepBudget,
	})
	resp, err := chat.Chat(ctx, llmprovider.ChatRequest{
		Model:           models.MemoryModel,
		ReasoningEffort: models.MemoryReasoningEffort,
		Messages: []llmprovider.Message{
			{
				Role: llmprovider.RoleSystem,
				Content: identity + fmt.Sprintf(
					"\nMerge previous_memo and segment into a concise, factual running memory summary preserving concrete facts, promises, and context. Stay under %d tokens.",
					keepBudget,
				),
			},
			{
				Role:    llmprovider.RoleUser,
				Content: string(summaryPayload),
			},
		},
	})
	if err != nil || resp == nil || len(resp.Choices) == 0 {
		return member, false, err
	}
	newMemo := strings.TrimSpace(resp.Choices[0].Message.TextContent())
	if newMemo == "" {
		return member, false, nil
	}

	newCompactedSeq := older[len(older)-1].Seq
	err = rdb.Tx(ctx, func(tx storage.RDBTx) error {
		member.ActorState.CompressedMemo = newMemo
		member.ActorState.CompactedThroughSeq = newCompactedSeq
		if err := tx.Rooms().UpsertMember(ctx, member); err != nil {
			return err
		}
		if len(candidates) > 0 {
			candJSON, _ := json.Marshal(candidates)
			if err := tx.Memories().CreateValueJob(ctx, domain.ValueJob{
				ID:                 domain.NewID(),
				RoomID:             room.ID,
				CharacterID:        member.CharacterID,
				SourceTurnID:       turnID,
				BaseValuesRevision: member.ActorState.ValuesRevision,
				CandidatesJSON:     string(candJSON),
				Status:             domain.ValueJobQueued,
				NextRunAt:          domain.NowMS(),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return member, false, err
	}
	return member, true, nil
}

func buildRecentExcerpts(all []domain.Message, actorID string, maxItems int) ([]memory.RecentExcerpt, map[string]struct{}) {
	visible := make([]domain.Message, 0, len(all))
	for _, m := range all {
		if m.Role == domain.MessageRoleSystem || !m.ObservedByActor(actorID) {
			continue
		}
		visible = append(visible, m)
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i].Seq < visible[j].Seq })
	if len(visible) > maxItems {
		visible = visible[len(visible)-maxItems:]
	}
	excerpts := make([]memory.RecentExcerpt, len(visible))
	excludeIDs := make(map[string]struct{}, len(visible))
	for i, m := range visible {
		excerpts[i] = memory.RecentExcerpt{
			AuthorName: m.AuthorName,
			Content:    m.Content,
			CreatedAt:  m.CreatedAt,
		}
		excludeIDs[m.ID] = struct{}{}
	}
	return excerpts, excludeIDs
}
