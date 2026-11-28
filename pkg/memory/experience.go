package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"strings"
	"time"

	sdk "github.com/snowmerak/decisions"
	"github.com/snowmerak/q/v2/pkg/domain"
	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
	"github.com/snowmerak/q/v2/pkg/llm/decisions"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/outbox"
)

const (
	maxExtractedMemories  = 8
	maxExtractedTextRunes = 400
)

// ExtractMemoriesParams supplies the confirmed turn context for episodic extraction.
type ExtractMemoriesParams struct {
	RoomID          string
	ActorID         string
	ActorName       string
	TurnID          string
	Identity        string
	MemoryModel     string
	ReasoningEffort string
	ConfirmedText   string
	ObservedBy      []string
	NowMS           int64
}

// ExtractAndStoreMemories asks the memory model to extract up to 8 concrete
// episodic memories from the confirmed turn and persists them + index_outbox entries atomically.
func ExtractAndStoreMemories(ctx context.Context, rdb storage.RDBStore, chat ChatCompleter, params ExtractMemoriesParams) ([]domain.Memory, error) {
	if rdb == nil || chat == nil || strings.TrimSpace(params.MemoryModel) == "" || strings.TrimSpace(params.ConfirmedText) == "" {
		return nil, nil
	}
	nowMS := params.NowMS
	if nowMS == 0 {
		nowMS = domain.NowMS()
	}
	nowISO := time.UnixMilli(nowMS).UTC().Format(time.RFC3339)

	userPayload, _ := json.Marshal(map[string]any{
		"now":       nowISO,
		"actor":     params.ActorName,
		"confirmed": params.ConfirmedText,
	})

	resp, err := chat.Chat(ctx, llmprovider.ChatRequest{
		Model:           params.MemoryModel,
		ReasoningEffort: params.ReasoningEffort,
		Messages: []llmprovider.Message{
			{
				Role: llmprovider.RoleSystem,
				Content: params.Identity +
					"\nExtract concrete personal episodic memories directly experienced in the confirmed interaction as a JSON array of up to 8 items: " +
					"[{\"occurredAt\":\"RFC3339 timestamp\",\"text\":\"concise factual summary\"}]. " +
					"Do not invent unconfirmed events or emotional speculation. Return [] if there is nothing worth remembering.",
			},
			{
				Role:    llmprovider.RoleUser,
				Content: string(userPayload),
			},
		},
	})
	if err != nil || resp == nil || len(resp.Choices) == 0 {
		return nil, err
	}

	raw := ExtractJSON(resp.Choices[0].Message.TextContent())
	var extracted []struct {
		OccurredAt string `json:"occurredAt"`
		Text       string `json:"text"`
	}
	if err := json.Unmarshal([]byte(raw), &extracted); err != nil {
		return nil, fmt.Errorf("memory: decode extracted memories: %w", err)
	}
	if len(extracted) == 0 {
		return nil, nil
	}
	if len(extracted) > maxExtractedMemories {
		extracted = extracted[:maxExtractedMemories]
	}

	observed := append([]string(nil), params.ObservedBy...)
	if len(observed) == 0 {
		observed = []string{params.ActorID}
	}

	var stored []domain.Memory
	err = rdb.Tx(ctx, func(tx storage.RDBTx) error {
		for _, item := range extracted {
			txt := strings.TrimSpace(item.Text)
			if txt == "" {
				continue
			}
			txt, _ = TruncateRunes(txt, maxExtractedTextRunes)
			occMS := nowMS
			if parsed, ok := parseOptionalISO(item.OccurredAt); ok {
				occMS = parsed
			}
			mem := domain.Memory{
				ID:           domain.NewID(),
				RoomID:       params.RoomID,
				OwnerID:      params.ActorID,
				SourceTurnID: params.TurnID,
				Kind:         domain.MemoryKindEpisodic,
				Content:      txt,
				ObservedBy:   observed,
				OccurredAt:   occMS,
				Status:       domain.MemoryStatusActive,
				Revision:     1,
				CreatedAt:    domain.NowMS(),
			}
			if err := tx.Memories().CreateMemory(ctx, mem); err != nil {
				return err
			}
			payloadBytes, err := json.Marshal(outbox.Payload{
				Scope:      "room:" + params.RoomID,
				OwnerID:    params.ActorID,
				ObservedBy: observed,
				Text:       txt,
				OccurredAt: occMS,
			})
			if err != nil {
				return err
			}
			if _, err := tx.Outbox().Enqueue(ctx, domain.OutboxEntry{
				EntityType:  "memory",
				EntityID:    mem.ID,
				Op:          domain.OutboxOpUpsert,
				Revision:    mem.Revision,
				PayloadJSON: string(payloadBytes),
				Status:      domain.OutboxStatusPending,
			}); err != nil {
				return err
			}
			stored = append(stored, mem)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stored, nil
}

// AppraiseStateParams supplies confirmed turn context for Decisions state_update appraisal.
type AppraiseStateParams struct {
	ActorName     string
	DecisionModel string
	CurrentState  domain.ActorState
	TargetIDs     []string
	ConfirmedText string
	NowMS         int64
}

var appraisalScoreLevels = []sdk.Level{
	{Label: "-20", Description: "Strong negative shift (-20)"},
	{Label: "-10", Description: "Mild negative shift (-10)"},
	{Label: "0", Description: "No change (0)"},
	{Label: "+10", Description: "Mild positive shift (+10)"},
	{Label: "+20", Description: "Strong positive shift (+20)"},
}

// AppraiseState evaluates Evernight's 5-level state_update Decisions questions
// for self and observed targets, clamping all axes to [-100, 100].
func AppraiseState(ctx context.Context, dec DecisionEvaluator, params AppraiseStateParams) (domain.ActorState, bool, error) {
	if dec == nil || strings.TrimSpace(params.DecisionModel) == "" || strings.TrimSpace(params.ConfirmedText) == "" {
		return params.CurrentState, false, nil
	}
	nowMS := params.NowMS
	if nowMS == 0 {
		nowMS = domain.NowMS()
	}

	targets := make([]string, 0, 1+len(params.TargetIDs))
	targets = append(targets, "self")
	for _, tid := range params.TargetIDs {
		if tid != "" && tid != "self" {
			targets = append(targets, tid)
		}
	}

	questions := make(sdk.Questions)
	for _, target := range targets {
		descTarget := "current internal state"
		if target != "self" {
			descTarget = "state toward " + target
		}
		questions[target+"_change"] = sdk.NoulQuestion{
			Instructions: fmt.Sprintf("Did %s's %s change as a result of this confirmed interaction? Return false if unchanged.", params.ActorName, descTarget),
		}
		questions[target+"_valence"] = sdk.ScoreQuestion{
			Instructions: fmt.Sprintf("Valence delta for %s -> %s ordered from -20 to +20. Pick 0 if unchanged.", params.ActorName, target),
			Levels:       appraisalScoreLevels,
		}
		questions[target+"_activation"] = sdk.ScoreQuestion{
			Instructions: fmt.Sprintf("Activation/arousal delta for %s -> %s ordered from -20 to +20. Pick 0 if unchanged.", params.ActorName, target),
			Levels:       appraisalScoreLevels,
		}
		if target != "self" {
			questions[target+"_trust"] = sdk.ScoreQuestion{
				Instructions: fmt.Sprintf("Trust delta for %s -> %s ordered from -20 to +20 independently of valence. Pick 0 if unchanged.", params.ActorName, target),
				Levels:       appraisalScoreLevels,
			}
		}
		questions[target+"_stress"] = sdk.NoulQuestion{
			Instructions: fmt.Sprintf("Is %s feeling stressed (%s) right now?", params.ActorName, descTarget),
		}
	}

	resp, err := dec.Evaluate(ctx, params.DecisionModel, sdk.Input{
		State: map[string]any{
			"actor":     params.ActorName,
			"state":     params.CurrentState,
			"confirmed": params.ConfirmedText,
		},
	}, questions)
	if err != nil {
		return params.CurrentState, false, err
	}

	next := cloneActorState(params.CurrentState)
	changed := false
	for _, target := range targets {
		pos, err := decisions.Positive(resp, target+"_change")
		if err != nil || !pos {
			continue
		}
		valDelta := scoreDelta(resp, target+"_valence")
		actDelta := scoreDelta(resp, target+"_activation")
		stressed, _ := decisions.Positive(resp, target+"_stress")

		if target == "self" {
			next.Self.Valence = clampAxis(next.Self.Valence + valDelta)
			next.Self.Activation = clampAxis(next.Self.Activation + actDelta)
			next.Self.Stressed = stressed
			next.StateUpdatedAt = nowMS
			changed = true
			continue
		}

		if next.Relationships == nil {
			next.Relationships = make(map[string]domain.Relationship)
		}
		if next.RelationshipsUpdatedAt == nil {
			next.RelationshipsUpdatedAt = make(map[string]int64)
		}
		rel := next.Relationships[target]
		rel.Valence = clampAxis(rel.Valence + valDelta)
		rel.Activation = clampAxis(rel.Activation + actDelta)
		rel.Trust = clampAxis(rel.Trust + scoreDelta(resp, target+"_trust"))
		rel.Stressed = stressed
		next.Relationships[target] = rel
		next.RelationshipsUpdatedAt[target] = nowMS
		changed = true
	}

	return next, changed, nil
}

func scoreDelta(resp *sdk.Response, key string) int {
	if resp == nil {
		return 0
	}
	ans, ok := resp.Answers[key]
	if !ok {
		return 0
	}
	scoreAns, ok := ans.(sdk.ScoreAnswer)
	if !ok {
		return 0
	}
	return int(math.Round((scoreAns.Score - 2.0) * 10.0))
}

func clampAxis(v int) int {
	if v < -100 {
		return -100
	}
	if v > 100 {
		return 100
	}
	return v
}

func cloneActorState(src domain.ActorState) domain.ActorState {
	out := src
	if src.Relationships != nil {
		out.Relationships = maps.Clone(src.Relationships)
	}
	if src.RelationshipsUpdatedAt != nil {
		out.RelationshipsUpdatedAt = maps.Clone(src.RelationshipsUpdatedAt)
	}
	if src.Values != nil {
		out.Values = append([]domain.CharacterValue(nil), src.Values...)
	}
	return out
}
