package memory_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	sdk "github.com/snowmerak/decisions"
	"github.com/snowmerak/q/v2/pkg/domain"
	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
	"github.com/snowmerak/q/v2/pkg/memory"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/outbox"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/sqlite"
	"github.com/snowmerak/q/v2/pkg/storage/search/blevehnsw"
)

type stubChat struct {
	fn func(ctx context.Context, req llmprovider.ChatRequest) (*llmprovider.ChatResponse, error)
}

func (s *stubChat) Chat(ctx context.Context, req llmprovider.ChatRequest) (*llmprovider.ChatResponse, error) {
	return s.fn(ctx, req)
}

type stubModels map[string]llmprovider.Model

func (m stubModels) Model(_ context.Context, id string) (llmprovider.Model, error) {
	model, ok := m[id]
	if !ok {
		return llmprovider.Model{}, errors.New("not found")
	}
	return model, nil
}

type stubDecisions struct {
	fn func(ctx context.Context, modelID string, input sdk.Input, questions sdk.Questions) (*sdk.Response, error)
}

func (d *stubDecisions) Evaluate(ctx context.Context, modelID string, input sdk.Input, questions sdk.Questions) (*sdk.Response, error) {
	return d.fn(ctx, modelID, input, questions)
}

func openTestStores(t *testing.T) (storage.RDBStore, storage.SearchIndex) {
	t.Helper()
	dir := t.TempDir()
	rdb, err := sqlite.Open(t.Context(), filepath.Join(dir, "q.sqlite"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })

	idx, err := blevehnsw.Open(filepath.Join(dir, "index"))
	if err != nil {
		t.Fatalf("blevehnsw.Open: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	return rdb, idx
}

func TestRecallGateObserverIsolationAndBudgetFit(t *testing.T) {
	rdb, idx := openTestStores(t)
	ctx := t.Context()

	persona, err := rdb.Personas().Create(ctx, domain.Persona{Name: "Player"})
	if err != nil {
		t.Fatal(err)
	}
	char, err := rdb.Characters().Create(ctx, domain.Character{Name: "Hana"})
	if err != nil {
		t.Fatal(err)
	}
	room, err := rdb.Rooms().Create(ctx, domain.Room{
		Title:     "Test Room",
		Mode:      domain.RoomModeDirect,
		PersonaID: persona.ID,
	}, []domain.RoomMember{{
		CharacterID:       char.ID,
		CharacterSnapshot: char,
		Active:            true,
	}})
	if err != nil {
		t.Fatal(err)
	}

	// Visible episodic memory + hidden memory belonging to another actor.
	if err := rdb.Memories().CreateMemory(ctx, domain.Memory{
		ID:         "mem-visible",
		RoomID:     room.ID,
		OwnerID:    char.ID,
		Kind:       domain.MemoryKindEpisodic,
		Content:    "We buried the brass compass near the northern lighthouse.",
		ObservedBy: []string{char.ID},
		OccurredAt: 1000,
		Status:     domain.MemoryStatusActive,
		Revision:   1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Memories().CreateMemory(ctx, domain.Memory{
		ID:         "mem-secret",
		RoomID:     room.ID,
		OwnerID:    "other-actor",
		Kind:       domain.MemoryKindEpisodic,
		Content:    "Secret compass note from other actor.",
		ObservedBy: []string{"other-actor"},
		OccurredAt: 1000,
		Status:     domain.MemoryStatusActive,
		Revision:   1,
	}); err != nil {
		t.Fatal(err)
	}
	for _, doc := range []storage.SearchDocument{
		{
			ID:         "mem-visible",
			EntityType: "memory",
			Scope:      "room:" + room.ID,
			OwnerID:    char.ID,
			ObservedBy: []string{char.ID},
			Text:       "We buried the brass compass near the northern lighthouse.",
			OccurredAt: 1000,
			Revision:   1,
		},
		{
			ID:         "mem-secret",
			EntityType: "memory",
			Scope:      "room:" + room.ID,
			OwnerID:    "other-actor",
			ObservedBy: []string{"other-actor"},
			Text:       "Secret compass note from other actor.",
			OccurredAt: 1000,
			Revision:   1,
		},
	} {
		if err := idx.Upsert(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}

	// Gate false skips planner/search completely.
	plannerCalled := false
	depsGateFalse := memory.Deps{
		Chat: &stubChat{fn: func(context.Context, llmprovider.ChatRequest) (*llmprovider.ChatResponse, error) {
			plannerCalled = true
			return nil, nil
		}},
		Decisions: &stubDecisions{fn: func(_ context.Context, _ string, _ sdk.Input, q sdk.Questions) (*sdk.Response, error) {
			if _, ok := q["needs_recall"]; ok {
				return &sdk.Response{Answers: map[string]sdk.Answer{
					"needs_recall": sdk.NoulAnswer{Probability: 0.1},
				}}, nil
			}
			return nil, errors.New("unexpected question")
		}},
	}
	items, err := memory.Recall(ctx, rdb, idx, depsGateFalse, memory.RecallInput{
		RoomID:             room.ID,
		ActorID:            char.ID,
		ActorName:          char.Name,
		CurrentText:        "hello!",
		DecisionModel:      "d1",
		RecallPlannerModel: "m1",
		OwnBudget:          4000,
		NowMS:              2000,
	})
	if err != nil {
		t.Fatalf("Recall gate false: %v", err)
	}
	if plannerCalled || len(items) != 0 {
		t.Fatalf("expected recall skipped on gate=false, plannerCalled=%v items=%d", plannerCalled, len(items))
	}

	// Gate true uses minimal effort on planner and filters strictly by ObserverID.
	var capturedEffort string
	depsGateTrue := memory.Deps{
		Models: stubModels{
			"m1": {
				ID: "m1",
				Capabilities: &llmprovider.ModelCapabilities{
					Reasoning: &llmprovider.ReasoningCapabilities{
						Supported:        true,
						Control:          llmprovider.ReasoningControlEffort,
						SupportedEfforts: []string{"minimal", "low", "high"},
					},
				},
			},
		},
		Chat: &stubChat{fn: func(_ context.Context, req llmprovider.ChatRequest) (*llmprovider.ChatResponse, error) {
			capturedEffort = req.ReasoningEffort
			return &llmprovider.ChatResponse{
				Choices: []llmprovider.Choice{{
					Message: llmprovider.Message{
						Role:    llmprovider.RoleAssistant,
						Content: `{"queries":[{"query":"compass lighthouse","kind":"memory"}]}`,
					},
				}},
			}, nil
		}},
		Decisions: &stubDecisions{fn: func(_ context.Context, _ string, _ sdk.Input, q sdk.Questions) (*sdk.Response, error) {
			if _, ok := q["needs_recall"]; ok {
				return &sdk.Response{Answers: map[string]sdk.Answer{
					"needs_recall": sdk.NoulAnswer{Probability: 0.9},
				}}, nil
			}
			answers := make(map[string]sdk.Answer, len(q))
			for k := range q {
				answers[k] = sdk.NoulAnswer{Probability: 0.85}
			}
			return &sdk.Response{Answers: answers}, nil
		}},
	}
	items, err = memory.Recall(ctx, rdb, idx, depsGateTrue, memory.RecallInput{
		RoomID:             room.ID,
		ActorID:            char.ID,
		ActorName:          char.Name,
		CurrentText:        "Where did we hide the compass?",
		DecisionModel:      "d1",
		RecallPlannerModel: "m1",
		OwnBudget:          4000,
		NowMS:              2000,
	})
	if err != nil {
		t.Fatalf("Recall gate true: %v", err)
	}
	if capturedEffort != "minimal" {
		t.Fatalf("planner reasoning effort = %q, want minimal", capturedEffort)
	}
	if len(items) != 1 || items[0].ID != "mem-visible" {
		t.Fatalf("recalled items = %+v, want [mem-visible]", items)
	}
}

func TestExtractMemoriesAppraisalAndValueWorker(t *testing.T) {
	rdb, idx := openTestStores(t)
	ctx := t.Context()

	persona, err := rdb.Personas().Create(ctx, domain.Persona{Name: "Min"})
	if err != nil {
		t.Fatal(err)
	}
	char, err := rdb.Characters().Create(ctx, domain.Character{
		Name:          "Sora",
		InitialValues: []string{"Never abandon an ally in danger."},
	})
	if err != nil {
		t.Fatal(err)
	}
	room, err := rdb.Rooms().Create(ctx, domain.Room{
		Title:     "Harbor",
		Mode:      domain.RoomModeDirect,
		PersonaID: persona.ID,
	}, []domain.RoomMember{{
		CharacterID:       char.ID,
		CharacterSnapshot: char,
		Active:            true,
		ActorState: domain.ActorState{
			Self: domain.ActorAffect{Valence: 90, Activation: -95},
			Relationships: map[string]domain.Relationship{
				persona.ID: {Valence: 85, Activation: 0, Trust: 95},
			},
			Values:         []domain.CharacterValue{{ID: "v-1", Text: char.InitialValues[0]}},
			ValuesRevision: 1,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}

	chat := &stubChat{fn: func(_ context.Context, req llmprovider.ChatRequest) (*llmprovider.ChatResponse, error) {
		sys := req.Messages[0].Content
		if slices.ContainsFunc([]string{"personal episodic memories"}, func(s string) bool { return len(sys) > 0 && containsSubstr(sys, s) }) {
			return &llmprovider.ChatResponse{Choices: []llmprovider.Choice{{
				Message: llmprovider.Message{
					Role:    llmprovider.RoleAssistant,
					Content: `[{"occurredAt":"2026-11-28T10:00:00Z","text":"Shared tea on the pier during the storm."}]`,
				},
			}}}, nil
		}
		// Consolidation response: update v-1 and add a new value.
		return &llmprovider.ChatResponse{Choices: []llmprovider.Choice{{
			Message: llmprovider.Message{
				Role:    llmprovider.RoleAssistant,
				Content: `[{"id":"v-1","text":"Protect allies in danger unless they intentionally betray innocents."},{"id":null,"text":"Keep promises made before dark."}]`,
			},
		}}}, nil
	}}

	mems, err := memory.ExtractAndStoreMemories(ctx, rdb, chat, memory.ExtractMemoriesParams{
		RoomID:        room.ID,
		ActorID:       char.ID,
		ActorName:     char.Name,
		TurnID:        "turn-1",
		MemoryModel:   "m1",
		ConfirmedText: "User: We stayed through the storm.\nSora: We drank hot tea together.",
		ObservedBy:    []string{char.ID},
		NowMS:         1700000000000,
	})
	if err != nil {
		t.Fatalf("ExtractAndStoreMemories: %v", err)
	}
	if len(mems) != 1 {
		t.Fatalf("expected 1 stored memory, got %d", len(mems))
	}

	// Drain outbox into Bleve so indexing is verified too.
	w := outbox.NewWorker(rdb, idx, nil)
	if n, err := w.ProcessOnce(ctx); err != nil || n != 1 {
		t.Fatalf("outbox ProcessOnce = %d, err=%v", n, err)
	}

	// Appraisal clamping [-100, 100].
	dec := &stubDecisions{fn: func(_ context.Context, _ string, _ sdk.Input, q sdk.Questions) (*sdk.Response, error) {
		ans := make(map[string]sdk.Answer, len(q))
		for name := range q {
			switch {
			case containsSubstr(name, "_change"), containsSubstr(name, "_stress"):
				ans[name] = sdk.NoulAnswer{Probability: 0.9}
			case name == "self_valence", name == persona.ID+"_valence", name == persona.ID+"_trust":
				ans[name] = sdk.ScoreAnswer{Score: 4} // +20 -> clamped at 100
			case name == "self_activation":
				ans[name] = sdk.ScoreAnswer{Score: 0} // -20 -> clamped at -100
			default:
				ans[name] = sdk.ScoreAnswer{Score: 2}
			}
		}
		return &sdk.Response{Answers: ans}, nil
	}}
	members, _ := rdb.Rooms().ListMembers(ctx, room.ID)
	nextState, changed, err := memory.AppraiseState(ctx, dec, memory.AppraiseStateParams{
		ActorName:     char.Name,
		DecisionModel: "d1",
		CurrentState:  members[0].ActorState,
		TargetIDs:     []string{persona.ID},
		ConfirmedText: "We survived together.",
		NowMS:         1700000001000,
	})
	if err != nil || !changed {
		t.Fatalf("AppraiseState changed=%v err=%v", changed, err)
	}
	if nextState.Self.Valence != 100 || nextState.Self.Activation != -100 || !nextState.Self.Stressed {
		t.Fatalf("unexpected clamped self state: %+v", nextState.Self)
	}
	if rel := nextState.Relationships[persona.ID]; rel.Valence != 100 || rel.Trust != 100 {
		t.Fatalf("unexpected clamped relationship: %+v", rel)
	}

	// Enqueue value_job and run ValueWorker.
	candJSON, _ := json.Marshal([]string{"Keep promises even at personal cost."})
	if err := rdb.Memories().CreateValueJob(ctx, domain.ValueJob{
		ID:                 "vj-1",
		RoomID:             room.ID,
		CharacterID:        char.ID,
		SourceTurnID:       "turn-1",
		BaseValuesRevision: 1,
		CandidatesJSON:     string(candJSON),
		Status:             domain.ValueJobQueued,
		NextRunAt:          1,
	}); err != nil {
		t.Fatal(err)
	}
	vw := memory.NewValueWorker(rdb, func(context.Context, string, string) (memory.ChatCompleter, string, string, int, error) {
		return chat, "m1", "", 16384, nil
	})
	processed, err := vw.ProcessOnce(ctx)
	if err != nil || !processed {
		t.Fatalf("ValueWorker.ProcessOnce processed=%v err=%v", processed, err)
	}

	updatedMembers, _ := rdb.Rooms().ListMembers(ctx, room.ID)
	vals := updatedMembers[0].ActorState.Values
	if updatedMembers[0].ActorState.ValuesRevision != 2 || len(vals) != 2 {
		t.Fatalf("unexpected consolidated values rev=%d vals=%+v", updatedMembers[0].ActorState.ValuesRevision, vals)
	}
}

func containsSubstr(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}

func TestRecallIncludesScopedKnowledgeAndIsolatesPrivateScopes(t *testing.T) {
	rdb, idx := openTestStores(t)
	ctx := t.Context()

	persona, _ := rdb.Personas().Create(ctx, domain.Persona{Name: "Player"})
	char, _ := rdb.Characters().Create(ctx, domain.Character{Name: "Hana"})
	room, _ := rdb.Rooms().Create(ctx, domain.Room{
		Title: "Lab", Mode: domain.RoomModeDirect, PersonaID: persona.ID,
	}, []domain.RoomMember{{
		CharacterID: char.ID, CharacterSnapshot: char, Active: true,
	}})

	type kSpec struct {
		id    string
		scope string
		title string
		text  string
	}
	specs := []kSpec{
		{id: "k-global", scope: "global", title: "Global Manual", text: "quantum_beacon calibration code is cobalt"},
		{id: "k-room", scope: "room:" + room.ID, title: "Room Blueprint", text: "quantum_beacon sits on the third shelf"},
		{id: "k-char", scope: "character:" + char.ID, title: "Hana Personal Notes", text: "quantum_beacon battery replacement reminder"},
		{id: "k-other-room", scope: "room:other-room", title: "Other Room Secret", text: "quantum_beacon other room secret override"},
		{id: "k-other-char", scope: "character:other-char", title: "Other Char Secret", text: "quantum_beacon other character private key"},
	}

	for _, sp := range specs {
		doc, err := rdb.Knowledge().CreateDoc(ctx, domain.KnowledgeDoc{
			ID: sp.id, Scope: sp.scope, Title: sp.title, MediaType: "text/markdown",
			Content: sp.text, Revision: 1, Status: domain.KnowledgeStatusActive,
			CreatedAt: 1000, UpdatedAt: 1000,
		})
		if err != nil {
			t.Fatal(err)
		}
		chID := sp.id + "-c0"
		if err := rdb.Knowledge().ReplaceChunks(ctx, doc.ID, []domain.KnowledgeChunk{{
			ID: chID, DocID: doc.ID, DocRevision: 1, ChunkOrdinal: 0, CharOffset: 0, Text: sp.text,
		}}); err != nil {
			t.Fatal(err)
		}
		if err := idx.Upsert(ctx, storage.SearchDocument{
			ID: chID, EntityType: "knowledge_chunk", Scope: sp.scope, OwnerID: doc.ID,
			Text: sp.title + "\n\n" + sp.text, OccurredAt: 1000, Revision: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}

	items, err := memory.Recall(ctx, rdb, idx, memory.Deps{}, memory.RecallInput{
		RoomID:      room.ID,
		ActorID:     char.ID,
		ActorName:   char.Name,
		CurrentText: "What do we know about quantum_beacon?",
		OwnBudget:   4000,
		NowMS:       5000,
	})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	gotIDs := make(map[string]bool, len(items))
	for _, it := range items {
		gotIDs[it.OwnerID] = true
		if it.Kind != "knowledge" {
			t.Fatalf("expected knowledge item, got %+v", it)
		}
	}
	for _, want := range []string{"k-global", "k-room", "k-char"} {
		if !gotIDs[want] {
			t.Fatalf("expected visible knowledge %s in recall results, got %+v", want, items)
		}
	}
	for _, forbid := range []string{"k-other-room", "k-other-char"} {
		if gotIDs[forbid] {
			t.Fatalf("private scoped knowledge %s leaked into recall results: %+v", forbid, items)
		}
	}
}

func TestRecallCrossRoomMemoriesAndSpeechWithObserverIsolation(t *testing.T) {
	rdb, idx := openTestStores(t)
	ctx := t.Context()

	persona, _ := rdb.Personas().Create(ctx, domain.Persona{Name: "Player"})
	charA, _ := rdb.Characters().Create(ctx, domain.Character{Name: "Hana"})
	charB, _ := rdb.Characters().Create(ctx, domain.Character{Name: "Doyun"})

	room1, _ := rdb.Rooms().Create(ctx, domain.Room{
		Title: "Harbor Cafe", Mode: domain.RoomModeGroup, PersonaID: persona.ID,
	}, []domain.RoomMember{
		{CharacterID: charA.ID, CharacterSnapshot: charA, Active: true},
		{CharacterID: charB.ID, CharacterSnapshot: charB, Active: true},
	})
	room2, _ := rdb.Rooms().Create(ctx, domain.Room{
		Title: "Studio Desk", Mode: domain.RoomModeDirect, PersonaID: persona.ID,
	}, []domain.RoomMember{
		{CharacterID: charA.ID, CharacterSnapshot: charA, Active: true},
	})

	turn1, _, err := rdb.Turns().CreateOrGetIdempotent(ctx, domain.Turn{
		RoomID: room1.ID, RequestID: "r1-t1", InputHash: "h1", Status: domain.TurnStatusCompleted,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Visible memory & dialogue observed by charA in room1
	if err := rdb.Memories().CreateMemory(ctx, domain.Memory{
		ID: "mem-cross-vis", RoomID: room1.ID, OwnerID: charA.ID, SourceTurnID: turn1.ID,
		Kind: domain.MemoryKindEpisodic, Content: "Harbor lighthouse key is taped under the espresso drawer.",
		ObservedBy: []string{charA.ID}, OccurredAt: 1000, Status: domain.MemoryStatusActive, Revision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := idx.Upsert(ctx, storage.SearchDocument{
		ID: "mem-cross-vis", EntityType: "memory", Scope: "room:" + room1.ID, OwnerID: charA.ID,
		ObservedBy: []string{charA.ID}, Text: "Harbor lighthouse key is taped under the espresso drawer.", OccurredAt: 1000, Revision: 1,
	}); err != nil {
		t.Fatal(err)
	}

	msgVis, err := rdb.Messages().Append(ctx, domain.Message{
		RoomID: room1.ID, TurnID: turn1.ID, Role: domain.MessageRoleUser, AuthorID: persona.ID, AuthorName: "Player",
		Content: "Don't forget the harbor lighthouse key code is 4419.", ObservedBy: []string{persona.ID, charA.ID}, CreatedAt: 1200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.Upsert(ctx, storage.SearchDocument{
		ID: msgVis.ID, EntityType: "message", Scope: "room:" + room1.ID, OwnerID: persona.ID,
		ObservedBy: []string{persona.ID, charA.ID}, Text: msgVis.Content, OccurredAt: 1200, Revision: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// Private memory & message in room1 only observed by charB (must never leak to charA in room2)
	if err := rdb.Memories().CreateMemory(ctx, domain.Memory{
		ID: "mem-cross-priv", RoomID: room1.ID, OwnerID: charB.ID, SourceTurnID: turn1.ID,
		Kind: domain.MemoryKindEpisodic, Content: "Secret harbor lighthouse vault bypass is 9999.",
		ObservedBy: []string{charB.ID}, OccurredAt: 1100, Status: domain.MemoryStatusActive, Revision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := idx.Upsert(ctx, storage.SearchDocument{
		ID: "mem-cross-priv", EntityType: "memory", Scope: "room:" + room1.ID, OwnerID: charB.ID,
		ObservedBy: []string{charB.ID}, Text: "Secret harbor lighthouse vault bypass is 9999.", OccurredAt: 1100, Revision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	msgPriv, err := rdb.Messages().Append(ctx, domain.Message{
		RoomID: room1.ID, TurnID: turn1.ID, Role: domain.MessageRoleCharacter, AuthorID: charB.ID, AuthorName: "Doyun",
		Content: "Whispered harbor lighthouse vault secret 9999.", ObservedBy: []string{charB.ID}, CreatedAt: 1300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.Upsert(ctx, storage.SearchDocument{
		ID: msgPriv.ID, EntityType: "message", Scope: "room:" + room1.ID, OwnerID: charB.ID,
		ObservedBy: []string{charB.ID}, Text: msgPriv.Content, OccurredAt: 1300, Revision: 1,
	}); err != nil {
		t.Fatal(err)
	}

	items, err := memory.Recall(ctx, rdb, idx, memory.Deps{}, memory.RecallInput{
		RoomID:      room2.ID,
		ActorID:     charA.ID,
		ActorName:   charA.Name,
		CurrentText: "What was the harbor lighthouse key code?",
		OwnBudget:   4000,
		NowMS:       5000,
	})
	if err != nil {
		t.Fatalf("Recall in room2: %v", err)
	}

	got := make(map[string]memory.RecalledItem, len(items))
	for _, it := range items {
		got[it.ID] = it
	}
	if it, ok := got["mem-cross-vis"]; !ok || it.RoomID != room1.ID || it.RoomTitle != "Harbor Cafe" {
		t.Fatalf("expected cross-room memory mem-cross-vis with RoomTitle 'Harbor Cafe', got %+v", items)
	}
	if it, ok := got[msgVis.ID]; !ok || it.RoomID != room1.ID || it.RoomTitle != "Harbor Cafe" {
		t.Fatalf("expected cross-room observed speech %s with RoomTitle 'Harbor Cafe', got %+v", msgVis.ID, items)
	}
	if _, leaked := got["mem-cross-priv"]; leaked {
		t.Fatalf("unobserved memory leaked across rooms: %+v", items)
	}
	if _, leaked := got[msgPriv.ID]; leaked {
		t.Fatalf("unobserved message leaked across rooms: %+v", items)
	}

	// Verify pagination: append 25 newer unobserved messages in room1 (so older observed msgVis falls outside top 20)
	// and recall again from room2 (which has zero local messages and zero memories).
	for i := range 25 {
		mUnobs, err := rdb.Messages().Append(ctx, domain.Message{
			RoomID: room1.ID, TurnID: turn1.ID, Role: domain.MessageRoleCharacter, AuthorID: charB.ID, AuthorName: "Doyun",
			Content: "Unrelated later chatter", ObservedBy: []string{charB.ID}, CreatedAt: int64(2000 + i),
		})
		if err != nil {
			t.Fatal(err)
		}
		_ = idx.Upsert(ctx, storage.SearchDocument{
			ID: mUnobs.ID, EntityType: "message", Scope: "room:" + room1.ID, OwnerID: charB.ID,
			ObservedBy: []string{charB.ID}, Text: mUnobs.Content, OccurredAt: mUnobs.CreatedAt, Revision: 1,
		})
	}
	// Soft-archive the episodic memory so recallable detection depends strictly on finding old observed message msgVis.
	if err := rdb.Memories().UpdateMemory(ctx, domain.Memory{
		ID: "mem-cross-vis", RoomID: room1.ID, OwnerID: charA.ID, SourceTurnID: turn1.ID,
		Kind: domain.MemoryKindEpisodic, Content: "Harbor lighthouse key is taped under the espresso drawer.",
		ObservedBy: []string{charA.ID}, OccurredAt: 1000, Status: domain.MemoryStatusSuperseded, Revision: 2,
	}); err != nil {
		t.Fatal(err)
	}
	itemsOld, err := memory.Recall(ctx, rdb, idx, memory.Deps{}, memory.RecallInput{
		RoomID:      room2.ID,
		ActorID:     charA.ID,
		ActorName:   charA.Name,
		CurrentText: "What was the harbor lighthouse key code?",
		OwnBudget:   4000,
		NowMS:       5000,
	})
	if err != nil {
		t.Fatalf("Recall older cross-room msg: %v", err)
	}
	foundOld := false
	for _, it := range itemsOld {
		if it.ID == msgVis.ID {
			foundOld = true
			break
		}
	}
	if !foundOld {
		t.Fatalf("expected older cross-room message %s recalled past >20 newer unobserved messages, got %+v", msgVis.ID, itemsOld)
	}
}
