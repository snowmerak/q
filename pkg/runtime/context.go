package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
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
		ChatModel:             strings.TrimSpace(cfg.LLM.Roles.ChatModel),
		MemoryModel:           strings.TrimSpace(cfg.LLM.Roles.MemoryModel),
		RecallPlannerModel:    strings.TrimSpace(cfg.LLM.Roles.RecallPlannerModel),
		DecisionModel:         strings.TrimSpace(cfg.Decisions.DefaultModelID),
		ReasoningEffort:       strings.TrimSpace(cfg.LLM.Roles.ChatReasoningEffort),
		MemoryReasoningEffort: strings.TrimSpace(cfg.LLM.Roles.MemoryReasoningEffort),
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

func snapshotPromptState(room domain.Room, state domain.ActorState) domain.PromptSnapshotState {
	var rels map[string]domain.Relationship
	if len(state.Relationships) > 0 {
		rels = maps.Clone(state.Relationships)
	}
	var vals []domain.CharacterValue
	if len(state.Values) > 0 {
		vals = slices.Clone(state.Values)
	}
	return domain.PromptSnapshotState{
		Self:          state.Self,
		Relationships: rels,
		Values:        vals,
		WorkDir:       strings.TrimSpace(room.WorkDir),
	}
}

func clonePromptSnapshot(src *domain.PromptSnapshotState) *domain.PromptSnapshotState {
	if src == nil {
		return nil
	}
	out := *src
	if len(src.Relationships) > 0 {
		out.Relationships = maps.Clone(src.Relationships)
	}
	if len(src.Values) > 0 {
		out.Values = slices.Clone(src.Values)
	}
	return &out
}

func clonePromptCache(src *domain.PromptCacheState) *domain.PromptCacheState {
	if src == nil {
		return nil
	}
	out := &domain.PromptCacheState{
		EpochSeq:    src.EpochSeq,
		Baseline:    clonePromptSnapshot(src.Baseline),
		LastEmitted: clonePromptSnapshot(src.LastEmitted),
	}
	if len(src.Injections) > 0 {
		out.Injections = slices.Clone(src.Injections)
	}
	return out
}

func formatRecalledSection(recalled []memory.RecalledItem) string {
	if len(recalled) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Recalled Memories, Earlier Dialogue & Reference Knowledge:\n")
	for _, item := range recalled {
		if item.Kind == "knowledge" {
			if item.AuthorName != "" {
				b.WriteString(fmt.Sprintf("- [Knowledge: %s] %s\n", item.AuthorName, item.Text))
			} else {
				b.WriteString(fmt.Sprintf("- [Knowledge] %s\n", item.Text))
			}
			continue
		}
		ts := time.UnixMilli(item.OccurredAt).UTC().Format("2006-01-02 15:04")
		switch {
		case item.RoomTitle != "" && item.Kind == "speech" && item.AuthorName != "":
			b.WriteString(fmt.Sprintf("- [%s | Room: %s | %s] %s\n", ts, item.RoomTitle, item.AuthorName, item.Text))
		case item.RoomTitle != "":
			b.WriteString(fmt.Sprintf("- [%s | Room: %s] %s\n", ts, item.RoomTitle, item.Text))
		case item.Kind == "speech" && item.AuthorName != "":
			b.WriteString(fmt.Sprintf("- [%s | %s] %s\n", ts, item.AuthorName, item.Text))
		default:
			b.WriteString(fmt.Sprintf("- [%s] %s\n", ts, item.Text))
		}
	}
	return strings.TrimSpace(b.String())
}

func formatAffectLine(prefix string, a domain.ActorAffect) string {
	return fmt.Sprintf("%s: valence=%d, activation=%d, stressed=%t", prefix, a.Valence, a.Activation, a.Stressed)
}

func formatStateDelta(room domain.Room, prev, cur domain.PromptSnapshotState) string {
	var parts []string
	if prev.Self != cur.Self {
		parts = append(parts, formatAffectLine("Updated Internal Affect State", cur.Self))
	}
	if room.PersonaID != "" {
		prevRel, hadPrev := prev.Relationships[room.PersonaID]
		curRel, hasCur := cur.Relationships[room.PersonaID]
		if hasCur && (!hadPrev || prevRel != curRel) {
			name := room.PersonaSnapshot.Name
			if name == "" {
				name = "User"
			}
			parts = append(parts, fmt.Sprintf("Updated Relationship with %s: valence=%d, activation=%d, trust=%d, stressed=%t",
				name, curRel.Valence, curRel.Activation, curRel.Trust, curRel.Stressed))
		}
	}
	if !slices.Equal(prev.Values, cur.Values) {
		if vals := memory.RenderValues(cur.Values); vals != "" {
			parts = append(parts, "Updated Values:\n"+strings.TrimSpace(vals))
		} else {
			parts = append(parts, "Updated Values: (cleared)")
		}
	}
	if prev.WorkDir != cur.WorkDir {
		wd := cur.WorkDir
		if wd == "" {
			wd = "(none)"
		}
		parts = append(parts, "Updated Room Working Directory: "+wd)
	}
	return strings.Join(parts, "\n")
}

// PrepareAppendOnlyContext initializes or appends incremental turn-scoped context
// (state deltas + recalled memories) anchored to the latest visible message seq so
// earlier messages stay byte-for-byte immutable between compactions.
func PrepareAppendOnlyContext(
	ctx context.Context,
	rdb storage.RDBStore,
	room domain.Room,
	member domain.RoomMember,
	recalled []memory.RecalledItem,
	allMessages []domain.Message,
) (domain.RoomMember, error) {
	curSnap := snapshotPromptState(room, member.ActorState)
	dirty := false
	pc := member.ActorState.PromptCache
	if pc == nil || pc.Baseline == nil || pc.EpochSeq != member.ActorState.CompactedThroughSeq {
		pc = &domain.PromptCacheState{
			EpochSeq:    member.ActorState.CompactedThroughSeq,
			Baseline:    clonePromptSnapshot(&curSnap),
			LastEmitted: clonePromptSnapshot(&curSnap),
			Injections:  nil,
		}
		member.ActorState.PromptCache = pc
		dirty = true
	}

	var anchorSeq int64
	for _, m := range allMessages {
		if m.Seq <= member.ActorState.CompactedThroughSeq {
			continue
		}
		if !m.ObservedByActor(member.CharacterID) {
			continue
		}
		if m.Seq > anchorSeq {
			anchorSeq = m.Seq
		}
	}

	alreadyAnchored := false
	for _, inj := range pc.Injections {
		if inj.AfterSeq == anchorSeq {
			alreadyAnchored = true
			break
		}
	}
	if !alreadyAnchored {
		lastEmitted := pc.LastEmitted
		if lastEmitted == nil {
			lastEmitted = pc.Baseline
		}
		var sections []string
		if delta := formatStateDelta(room, *lastEmitted, curSnap); delta != "" {
			sections = append(sections, delta)
		}
		if recText := formatRecalledSection(recalled); recText != "" {
			sections = append(sections, recText)
		}
		if len(sections) > 0 {
			pc.Injections = append(pc.Injections, domain.PromptInjection{
				AfterSeq: anchorSeq,
				Content:  strings.Join(sections, "\n\n"),
			})
			pc.LastEmitted = clonePromptSnapshot(&curSnap)
			dirty = true
		}
	}

	if !dirty || rdb == nil {
		return member, nil
	}

	if err := rdb.Tx(ctx, func(tx storage.RDBTx) error {
		members, err := tx.Rooms().ListMembers(ctx, room.ID)
		if err != nil {
			return err
		}
		for _, cur := range members {
			if cur.CharacterID == member.CharacterID {
				cur.ActorState.PromptCache = member.ActorState.PromptCache
				if err := tx.Rooms().UpsertMember(ctx, cur); err != nil {
					return err
				}
				member = cur
				return nil
			}
		}
		return tx.Rooms().UpsertMember(ctx, member)
	}); err != nil {
		return member, err
	}
	return member, nil
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

	var baseline domain.PromptSnapshotState
	var injections []domain.PromptInjection
	usedPromptCache := false
	if member.ActorState.PromptCache != nil &&
		member.ActorState.PromptCache.Baseline != nil &&
		member.ActorState.PromptCache.EpochSeq == member.ActorState.CompactedThroughSeq {
		baseline = *member.ActorState.PromptCache.Baseline
		injections = member.ActorState.PromptCache.Injections
		usedPromptCache = true
	} else {
		baseline = snapshotPromptState(room, member.ActorState)
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
	if vals := memory.RenderValues(baseline.Values); vals != "" {
		stablePrefix.WriteByte('\n')
		stablePrefix.WriteString(vals)
	}
	if memo := strings.TrimSpace(member.ActorState.CompressedMemo); memo != "" {
		stablePrefix.WriteString("\nEarlier Conversation Summary:\n")
		stablePrefix.WriteString(memo)
		stablePrefix.WriteByte('\n')
	}
	stablePrefix.WriteByte('\n')
	stablePrefix.WriteString(formatAffectLine("Internal Affect State", baseline.Self))
	stablePrefix.WriteByte('\n')
	if rel, ok := baseline.Relationships[room.PersonaID]; ok {
		stablePrefix.WriteString("Relationship with ")
		stablePrefix.WriteString(room.PersonaSnapshot.Name)
		stablePrefix.WriteString(": valence=")
		stablePrefix.WriteString(strconv.Itoa(rel.Valence))
		stablePrefix.WriteString(", activation=")
		stablePrefix.WriteString(strconv.Itoa(rel.Activation))
		stablePrefix.WriteString(", trust=")
		stablePrefix.WriteString(strconv.Itoa(rel.Trust))
		stablePrefix.WriteString(", stressed=")
		stablePrefix.WriteString(strconv.FormatBool(rel.Stressed))
		stablePrefix.WriteByte('\n')
	}
	if baseline.WorkDir != "" {
		stablePrefix.WriteString("Room Working Directory: ")
		stablePrefix.WriteString(baseline.WorkDir)
		stablePrefix.WriteByte('\n')
	}

	msgs := make([]llmprovider.Message, 0, len(allMessages)+4)
	msgs = append(msgs, llmprovider.Message{
		Role:    llmprovider.RoleSystem,
		Content: strings.TrimSpace(stablePrefix.String()),
	})

	injBySeq := make(map[int64]string, len(injections))
	for _, inj := range injections {
		if strings.TrimSpace(inj.Content) != "" {
			injBySeq[inj.AfterSeq] = inj.Content
		}
	}
	if inj0, ok := injBySeq[0]; ok {
		msgs = append(msgs, llmprovider.Message{
			Role:    llmprovider.RoleUser,
			Content: "<turn_context>\n" + inj0 + "\n</turn_context>",
		})
	}

	visible := make([]domain.Message, 0, len(allMessages))
	var lastVisibleSeq int64
	for _, m := range allMessages {
		if m.Seq <= member.ActorState.CompactedThroughSeq {
			continue
		}
		if !m.ObservedByActor(member.CharacterID) {
			continue
		}
		lastVisibleSeq = m.Seq
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
		inj, hasInj := injBySeq[m.Seq]
		if hasInj && role == llmprovider.RoleUser {
			content += "\n\n<turn_context>\n" + inj + "\n</turn_context>"
			msgs = append(msgs, llmprovider.Message{
				Role:    role,
				Content: content,
			})
			continue
		}
		msgs = append(msgs, llmprovider.Message{
			Role:    role,
			Content: content,
		})
		if hasInj {
			msgs = append(msgs, llmprovider.Message{
				Role:    llmprovider.RoleUser,
				Content: "<turn_context>\n" + inj + "\n</turn_context>",
			})
		}
	}

	if _, anchorHasInj := injBySeq[lastVisibleSeq]; (!usedPromptCache || !anchorHasInj) && len(recalled) > 0 {
		if recallText := formatRecalledSection(recalled); recallText != "" {
			wrapped := "<turn_context>\n" + recallText + "\n</turn_context>"
			if n := len(msgs); n > 0 && msgs[n-1].Role == llmprovider.RoleUser {
				msgs[n-1].Content += "\n\n" + wrapped
			} else {
				msgs = append(msgs, llmprovider.Message{
					Role:    llmprovider.RoleUser,
					Content: wrapped,
				})
			}
		}
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
	previewMember := member
	previewMember.ActorState.PromptCache = clonePromptCache(member.ActorState.PromptCache)
	if prepped, prepErr := PrepareAppendOnlyContext(ctx, nil, room, previewMember, recalled, allMessages); prepErr == nil {
		previewMember = prepped
	}
	assembled, err := BuildActorPrompt(room, previewMember, recalled, allMessages, models.ContextWindow)
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
		cur := member
		if freshMembers, listErr := tx.Rooms().ListMembers(ctx, room.ID); listErr == nil {
			for _, m := range freshMembers {
				if m.CharacterID == member.CharacterID {
					cur = m
					break
				}
			}
		}
		cur.ActorState.CompressedMemo = newMemo
		cur.ActorState.CompactedThroughSeq = newCompactedSeq
		newSnap := snapshotPromptState(room, cur.ActorState)
		cur.ActorState.PromptCache = &domain.PromptCacheState{
			EpochSeq:    newCompactedSeq,
			Baseline:    &newSnap,
			LastEmitted: clonePromptSnapshot(&newSnap),
			Injections:  nil,
		}
		if err := tx.Rooms().UpsertMember(ctx, cur); err != nil {
			return err
		}
		if len(candidates) > 0 {
			candJSON, _ := json.Marshal(candidates)
			if err := tx.Memories().CreateValueJob(ctx, domain.ValueJob{
				ID:                 domain.NewID(),
				RoomID:             room.ID,
				CharacterID:        cur.CharacterID,
				SourceTurnID:       turnID,
				BaseValuesRevision: cur.ActorState.ValuesRevision,
				CandidatesJSON:     string(candJSON),
				Status:             domain.ValueJobQueued,
				NextRunAt:          domain.NowMS(),
			}); err != nil {
				return err
			}
		}
		member = cur
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
