package memory

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	sdk "github.com/snowmerak/decisions"
	"github.com/snowmerak/q/v2/pkg/domain"
	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
	"github.com/snowmerak/q/v2/pkg/llm/decisions"
	"github.com/snowmerak/q/v2/pkg/storage"
)

const (
	maxRecallQueries     = 3
	maxRecallCandidates  = 16
	maxRecalledItems     = 8
	maxRecallItemRunes   = 1200
	recallBudgetFraction = 0.10
)

type RecentExcerpt struct {
	AuthorName string `json:"author_name"`
	Content    string `json:"content"`
	CreatedAt  int64  `json:"created_at"`
}

type RecallInput struct {
	RoomID             string
	ActorID            string
	ActorName          string
	UserName           string
	CurrentText        string
	Recent             []RecentExcerpt
	ExcludeIDs         map[string]struct{}
	DecisionModel      string
	RecallPlannerModel string
	OwnBudget          int
	NowMS              int64
}

type RecallQuery struct {
	Query   string `json:"query,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Kind    string `json:"kind,omitempty"`    // "memory" | "speech"
	Speaker string `json:"speaker,omitempty"` // "user" | "self"
}

type RecalledItem struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"` // "memory" | "speech" | "knowledge"
	OwnerID     string  `json:"owner_id"`
	RoomID      string  `json:"room_id,omitempty"`
	RoomTitle   string  `json:"room_title,omitempty"`
	AuthorName  string  `json:"author_name,omitempty"`
	OccurredAt  int64   `json:"occurred_at"`
	Text        string  `json:"text"`
	Probability float64 `json:"probability,omitempty"`
	Truncated   bool    `json:"truncated,omitempty"`
}

func resolveRoomTitle(ctx context.Context, rdb storage.RDBStore, cache map[string]string, roomID string) string {
	if roomID == "" {
		return ""
	}
	if title, ok := cache[roomID]; ok {
		return title
	}
	title := "Another Room"
	if rm, err := rdb.Rooms().Get(ctx, roomID); err == nil && strings.TrimSpace(rm.Title) != "" {
		title = strings.TrimSpace(rm.Title)
	}
	cache[roomID] = title
	return title
}

// KnowledgeScopeVisible reports whether a KnowledgeDoc with scope should be visible
// inside (roomID, actorID). Scopes prefixed with "room:" or "character:" are
// isolated to that specific room or character; all other scopes (including
// "global" and custom shared tags) are visible across rooms and actors.
func KnowledgeScopeVisible(scope, roomID, actorID string) bool {
	sc := strings.TrimSpace(scope)
	if strings.HasPrefix(sc, "room:") {
		return roomID != "" && sc == "room:"+roomID
	}
	if strings.HasPrefix(sc, "character:") {
		return actorID != "" && sc == "character:"+actorID
	}
	return true
}

// Recall executes the gated, observer-isolated memory/speech recall pipeline
// plus scoped reference knowledge retrieval:
// existence check -> memory_recall_gate -> lightweight query planner -> hybrid search -> memory_recall_select -> 10% budget fit.
func Recall(ctx context.Context, rdb storage.RDBStore, search storage.SearchIndex, deps Deps, input RecallInput) ([]RecalledItem, error) {
	if rdb == nil || search == nil || input.OwnBudget <= 0 || strings.TrimSpace(input.CurrentText) == "" {
		return nil, nil
	}
	if !hasRecallableData(ctx, rdb, input.RoomID, input.ActorID, input.ExcludeIDs) {
		return nil, nil
	}

	// Step 1: memory_recall_gate Decisions check.
	if deps.Decisions != nil && strings.TrimSpace(input.DecisionModel) != "" {
		gateResp, err := deps.Decisions.Evaluate(ctx, input.DecisionModel, sdk.Input{
			State: map[string]any{
				"actor":   input.ActorName,
				"recent":  input.Recent,
				"current": input.CurrentText,
			},
		}, sdk.Questions{
			"needs_recall": sdk.NoulQuestion{
				Instructions: "Does `current` require looking up past memories, prior events, earlier quotes, or reference knowledge documents not already answered in `recent`? Treat `recent` as context only. Return true if `current` asks about or refers to past times, earlier promises, items, places, domain facts, or missing details. Return false for greetings, acknowledgments, casual small talk, or purely present-moment replies.",
			},
		})
		if err == nil {
			if positive, posErr := decisions.Positive(gateResp, "needs_recall"); posErr == nil && !positive {
				return nil, nil
			}
		}
	}

	// Step 2: Lightweight query planner with fallback to raw input.
	queries, planned := planRecallQueries(ctx, deps, input)
	if !planned {
		queries = []RecallQuery{{Query: strings.TrimSpace(input.CurrentText)}}
	}
	if len(queries) == 0 {
		return nil, nil
	}

	// Step 3: SearchIndex queries with strict ObserverID filtering for memories/messages
	// (including cross-room memories/dialogue observed by ActorID) and scope visibility filtering for knowledge_chunk.
	type candidate struct {
		item  RecalledItem
		score float64
	}
	merged := make(map[string]candidate)
	roomTitles := make(map[string]string)

	for _, q := range queries {
		fromMS, hasFrom := parseOptionalISO(q.From)
		toMS, hasTo := parseOptionalISO(q.To)
		if hasFrom && hasTo && fromMS >= toMS {
			hasFrom, hasTo = false, false
		}

		mode := "lexical"
		var vec []float32
		if deps.Embedder != nil && strings.TrimSpace(q.Query) != "" {
			if v, embErr := deps.Embedder.EmbedQuery(ctx, q.Query); embErr == nil && len(v) > 0 {
				vec = v
				mode = "hybrid"
			}
		}

		var allHits []storage.SearchHit
		if q.Kind != "knowledge" {
			entityTypes := []string{"memory", "message"}
			switch q.Kind {
			case "memory":
				entityTypes = []string{"memory"}
			case "speech":
				entityTypes = []string{"message"}
			}
			if hits, err := search.Search(ctx, storage.SearchQuery{
				Text:        q.Query,
				Vector:      vec,
				Mode:        mode,
				EntityTypes: entityTypes,
				ObserverID:  input.ActorID,
				Limit:       maxRecallCandidates,
			}); err == nil {
				allHits = append(allHits, hits...)
			}
		}
		if (q.Kind == "" || q.Kind == "knowledge") && q.Speaker == "" {
			if kHits, err := search.Search(ctx, storage.SearchQuery{
				Text:        q.Query,
				Vector:      vec,
				Mode:        mode,
				EntityTypes: []string{"knowledge_chunk"},
				Limit:       maxRecallCandidates,
			}); err == nil {
				allHits = append(allHits, kHits...)
			}
		}

		for _, hit := range allHits {
			key := hit.EntityType + ":" + hit.ID
			switch hit.EntityType {
			case "memory":
				if q.Kind == "speech" || q.Kind == "knowledge" {
					continue
				}
				mem, err := rdb.Memories().GetMemory(ctx, hit.ID)
				if err != nil {
					continue
				}
				if mem.Status != domain.MemoryStatusActive || mem.OwnerID != input.ActorID {
					continue
				}
				if !slices.Contains(mem.ObservedBy, input.ActorID) {
					continue
				}
				if input.NowMS > 0 && mem.OccurredAt > input.NowMS {
					continue
				}
				if (hasFrom && mem.OccurredAt < fromMS) || (hasTo && mem.OccurredAt >= toMS) {
					continue
				}
				roomTitle := ""
				if mem.RoomID != "" && mem.RoomID != input.RoomID {
					roomTitle = resolveRoomTitle(ctx, rdb, roomTitles, mem.RoomID)
				}
				txt, truncated := TruncateRunes(mem.Content, maxRecallItemRunes)
				prev, exists := merged[key]
				if !exists || hit.Score > prev.score {
					merged[key] = candidate{
						score: hit.Score,
						item: RecalledItem{
							ID:         mem.ID,
							Kind:       "memory",
							OwnerID:    mem.OwnerID,
							RoomID:     mem.RoomID,
							RoomTitle:  roomTitle,
							OccurredAt: mem.OccurredAt,
							Text:       txt,
							Truncated:  truncated,
						},
					}
				}
			case "message":
				if q.Kind == "memory" || q.Kind == "knowledge" {
					continue
				}
				if _, excluded := input.ExcludeIDs[hit.ID]; excluded {
					continue
				}
				msg, err := rdb.Messages().Get(ctx, hit.ID)
				if err != nil {
					continue
				}
				if msg.Role == domain.MessageRoleSystem || !msg.ObservedByActor(input.ActorID) {
					continue
				}
				if q.Speaker == "self" && msg.AuthorID != input.ActorID {
					continue
				}
				if q.Speaker == "user" && msg.Role != domain.MessageRoleUser {
					continue
				}
				if input.NowMS > 0 && msg.CreatedAt > input.NowMS {
					continue
				}
				if (hasFrom && msg.CreatedAt < fromMS) || (hasTo && msg.CreatedAt >= toMS) {
					continue
				}
				roomTitle := ""
				if msg.RoomID != "" && msg.RoomID != input.RoomID {
					roomTitle = resolveRoomTitle(ctx, rdb, roomTitles, msg.RoomID)
				}
				txt, truncated := TruncateRunes(msg.Content, maxRecallItemRunes)
				prev, exists := merged[key]
				if !exists || hit.Score > prev.score {
					merged[key] = candidate{
						score: hit.Score,
						item: RecalledItem{
							ID:         msg.ID,
							Kind:       "speech",
							OwnerID:    msg.AuthorID,
							RoomID:     msg.RoomID,
							RoomTitle:  roomTitle,
							AuthorName: msg.AuthorName,
							OccurredAt: msg.CreatedAt,
							Text:       txt,
							Truncated:  truncated,
						},
					}
				}
			case "knowledge_chunk":
				if q.Kind == "memory" || q.Kind == "speech" || q.Speaker != "" {
					continue
				}
				ch, err := rdb.Knowledge().GetChunk(ctx, hit.ID)
				if err != nil {
					continue
				}
				doc, err := rdb.Knowledge().GetDoc(ctx, ch.DocID)
				if err != nil || doc.Status != domain.KnowledgeStatusActive || ch.DocRevision != doc.Revision {
					continue
				}
				if !KnowledgeScopeVisible(doc.Scope, input.RoomID, input.ActorID) {
					continue
				}
				if input.NowMS > 0 && doc.UpdatedAt > input.NowMS {
					continue
				}
				if (hasFrom && doc.UpdatedAt < fromMS) || (hasTo && doc.UpdatedAt >= toMS) {
					continue
				}
				txt, truncated := TruncateRunes(ch.Text, maxRecallItemRunes)
				prev, exists := merged[key]
				if !exists || hit.Score > prev.score {
					merged[key] = candidate{
						score: hit.Score,
						item: RecalledItem{
							ID:         ch.ID,
							Kind:       "knowledge",
							OwnerID:    doc.ID,
							AuthorName: doc.Title,
							OccurredAt: doc.UpdatedAt,
							Text:       txt,
							Truncated:  truncated,
						},
					}
				}
			}
		}
	}

	if len(merged) == 0 {
		return nil, nil
	}

	ranked := make([]candidate, 0, len(merged))
	for _, c := range merged {
		ranked = append(ranked, c)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].item.OccurredAt != ranked[j].item.OccurredAt {
			return ranked[i].item.OccurredAt > ranked[j].item.OccurredAt
		}
		return ranked[i].item.ID < ranked[j].item.ID
	})
	if len(ranked) > maxRecallCandidates {
		ranked = ranked[:maxRecallCandidates]
	}

	candidates := make([]RecalledItem, len(ranked))
	for i := range ranked {
		candidates[i] = ranked[i].item
	}

	// Step 4: memory_recall_select Decisions filter.
	selected := candidates
	if deps.Decisions != nil && strings.TrimSpace(input.DecisionModel) != "" {
		questions := make(sdk.Questions, len(candidates))
		evidenceCandidates := make([]map[string]any, len(candidates))
		for i, item := range candidates {
			key := candidateKey(i)
			questions[key] = sdk.NoulQuestion{
				Instructions: "Is this past memory, speech, or reference knowledge directly needed to answer or respond naturally to `current`? Select multiple if needed or reject all if irrelevant.",
			}
			evidenceCandidates[i] = map[string]any{
				"key":         key,
				"kind":        item.Kind,
				"author_name": item.AuthorName,
				"occurred_at": time.UnixMilli(item.OccurredAt).UTC().Format(time.RFC3339),
				"text":        item.Text,
			}
			if item.RoomTitle != "" {
				evidenceCandidates[i]["room_title"] = item.RoomTitle
			}
		}
		selResp, err := deps.Decisions.Evaluate(ctx, input.DecisionModel, sdk.Input{
			State: map[string]any{
				"current":    input.CurrentText,
				"candidates": evidenceCandidates,
			},
		}, questions)
		if err == nil && selResp != nil {
			filtered := make([]RecalledItem, 0, len(candidates))
			for i, item := range candidates {
				ans, ok := selResp.Answers[candidateKey(i)]
				if !ok {
					continue
				}
				noul, ok := ans.(sdk.NoulAnswer)
				if !ok || noul.Probability < 0.5 {
					continue
				}
				item.Probability = noul.Probability
				filtered = append(filtered, item)
			}
			sort.SliceStable(filtered, func(i, j int) bool {
				return filtered[i].Probability > filtered[j].Probability
			})
			selected = filtered
		}
	}

	// Step 5: Cap at 8 items and 10% of ownBudget tokens.
	return fitRecalledItems(selected, input.OwnBudget), nil
}

func hasRecallableData(ctx context.Context, rdb storage.RDBStore, roomID, actorID string, excludeIDs map[string]struct{}) bool {
	if mems, err := rdb.Memories().ListMemories(ctx, "", actorID, actorID, 1); err == nil && len(mems) > 0 {
		return true
	}
	if docs, err := rdb.Knowledge().ListDocs(ctx, ""); err == nil {
		for _, doc := range docs {
			if doc.Status == domain.KnowledgeStatusActive && KnowledgeScopeVisible(doc.Scope, roomID, actorID) {
				return true
			}
		}
	}
	if msgs, err := rdb.Messages().List(ctx, roomID, 0, len(excludeIDs)+12); err == nil {
		for _, msg := range msgs {
			if msg.Role == domain.MessageRoleSystem || !msg.ObservedByActor(actorID) {
				continue
			}
			if _, excluded := excludeIDs[msg.ID]; !excluded {
				return true
			}
		}
	}
	if rooms, err := rdb.Rooms().List(ctx); err == nil {
		for _, rm := range rooms {
			if rm.ID == roomID {
				continue
			}
			members, mErr := rdb.Rooms().ListMembers(ctx, rm.ID)
			if mErr != nil {
				continue
			}
			participates := false
			for _, m := range members {
				if m.CharacterID == actorID {
					participates = true
					break
				}
			}
			if !participates {
				continue
			}
			msgs, msgErr := rdb.Messages().List(ctx, rm.ID, 0, 20)
			if msgErr != nil {
				continue
			}
			for _, msg := range msgs {
				if msg.Role == domain.MessageRoleSystem || !msg.ObservedByActor(actorID) {
					continue
				}
				return true
			}
		}
	}
	return false
}

func planRecallQueries(ctx context.Context, deps Deps, input RecallInput) ([]RecallQuery, bool) {
	if deps.Chat == nil || strings.TrimSpace(input.RecallPlannerModel) == "" {
		return nil, false
	}
	effort := ""
	if deps.Models != nil {
		if meta, err := deps.Models.Model(ctx, input.RecallPlannerModel); err == nil && meta.Capabilities != nil && meta.Capabilities.Reasoning != nil {
			efforts := meta.Capabilities.Reasoning.SupportedEfforts
			switch {
			case slices.Contains(efforts, "minimal"):
				effort = "minimal"
			case slices.Contains(efforts, "low"):
				effort = "low"
			}
		}
	}

	nowISO := time.Now().UTC().Format(time.RFC3339)
	if input.NowMS > 0 {
		nowISO = time.UnixMilli(input.NowMS).UTC().Format(time.RFC3339)
	}
	payloadBytes, _ := json.Marshal(map[string]any{
		"actor":      input.ActorName,
		"user":       input.UserName,
		"now":        nowISO,
		"recent":     input.Recent,
		"current":    input.CurrentText,
		"maxQueries": maxRecallQueries,
	})

	req := llmprovider.ChatRequest{
		Model:           input.RecallPlannerModel,
		ReasoningEffort: effort,
		Messages: []llmprovider.Message{
			{
				Role: llmprovider.RoleSystem,
				Content: "Produce JSON only: {\"queries\":[{\"query\":\"short keywords\",\"from\":\"optional RFC3339\",\"to\":\"optional RFC3339\",\"kind\":\"memory|speech|knowledge\",\"speaker\":\"user|self\"}]}. " +
					"Return at most 3 focused search queries for past memories, dialogue, or reference knowledge, or {\"queries\":[]} if no lookup is needed.",
			},
			{
				Role:    llmprovider.RoleUser,
				Content: string(payloadBytes),
			},
		},
	}
	resp, err := deps.Chat.Chat(ctx, req)
	if err != nil || resp == nil || len(resp.Choices) == 0 {
		return nil, false
	}
	raw := ExtractJSON(resp.Choices[0].Message.TextContent())
	var decoded struct {
		Queries []RecallQuery `json:"queries"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, false
	}
	out := make([]RecallQuery, 0, maxRecallQueries)
	for _, q := range decoded.Queries {
		q.Query = strings.TrimSpace(q.Query)
		q.Kind = strings.ToLower(strings.TrimSpace(q.Kind))
		if q.Kind != "memory" && q.Kind != "speech" && q.Kind != "knowledge" {
			q.Kind = ""
		}
		q.Speaker = strings.ToLower(strings.TrimSpace(q.Speaker))
		if q.Speaker != "user" && q.Speaker != "self" {
			q.Speaker = ""
		}
		if q.Query == "" && q.From == "" && q.To == "" && q.Kind == "" && q.Speaker == "" {
			continue
		}
		out = append(out, q)
		if len(out) >= maxRecallQueries {
			break
		}
	}
	return out, true
}

func fitRecalledItems(items []RecalledItem, ownBudget int) []RecalledItem {
	budget := int(float64(ownBudget) * recallBudgetFraction)
	if budget <= 0 || len(items) == 0 {
		return nil
	}
	fitted := make([]RecalledItem, 0, min(len(items), maxRecalledItems))
	used := 0
	for _, item := range items {
		if len(fitted) >= maxRecalledItems {
			break
		}
		cost := EstimateTokens(item.Text) + 12
		if used+cost > budget {
			break
		}
		fitted = append(fitted, item)
		used += cost
	}
	return fitted
}

func parseOptionalISO(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return 0, false
		}
	}
	return t.UTC().UnixMilli(), true
}

func candidateKey(idx int) string {
	return "c" + string(rune('0'+idx/10)) + string(rune('0'+idx%10))
}
