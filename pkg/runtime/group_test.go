package runtime_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/q/v2/pkg/app/config"
	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/llm/decisions"
	"github.com/snowmerak/q/v2/pkg/llm/gateway"
	"github.com/snowmerak/q/v2/pkg/runtime"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/sqlite"
	"github.com/snowmerak/q/v2/pkg/storage/search/blevehnsw"
)

func TestGroupReserveActorSpeedAndAntiMonopoly(t *testing.T) {
	members := []domain.RoomMember{
		{CharacterID: "alice", BaseSpeed: 100, Active: true, CharacterSnapshot: domain.Character{Name: "Alice"}},
		{CharacterID: "bob", BaseSpeed: 50, Active: true, CharacterSnapshot: domain.Character{Name: "Bob"}},
		{CharacterID: "charlie", BaseSpeed: 200, Active: false, CharacterSnapshot: domain.Character{Name: "Charlie"}},
	}

	// Fast actor starts first, inactive actor is never picked.
	first, gauges := runtime.ReserveActor(members, nil, nil, []domain.Message{
		{Role: domain.MessageRoleUser, AuthorID: "user", Content: "Hello everyone", ObservedBy: []string{"alice", "bob"}},
	})
	if first == nil || first.CharacterID != "alice" {
		t.Fatalf("expected alice first, got %+v", first)
	}
	if gauges["bob"] <= 0 {
		t.Fatalf("expected bob gauge to advance, got %v", gauges)
	}

	// Simulate several consecutive steps with Alice speaking every time:
	// streak damping must allow Bob to catch up instead of letting Alice monopolize.
	msgs := []domain.Message{
		{Role: domain.MessageRoleUser, AuthorID: "user", Content: "Discuss", ObservedBy: []string{"alice", "bob"}},
		{Role: domain.MessageRoleCharacter, AuthorID: "alice", Content: "Point 1", ObservedBy: []string{"alice", "bob"}},
		{Role: domain.MessageRoleCharacter, AuthorID: "alice", Content: "Point 2", ObservedBy: []string{"alice", "bob"}},
	}
	second, _ := runtime.ReserveActor(members, gauges, nil, msgs)
	if second == nil || second.CharacterID != "bob" {
		t.Fatalf("expected streak damping to pick bob, got %+v", second)
	}

	// Mentioning Bob by name boosts Bob's speed even from zero gauges.
	mentioned, _ := runtime.ReserveActor(members, nil, nil, []domain.Message{
		{Role: domain.MessageRoleCharacter, AuthorID: "alice", Content: "Bob, what do you think?", ObservedBy: []string{"alice", "bob"}},
	})
	if mentioned == nil || mentioned.CharacterID != "bob" {
		t.Fatalf("expected mention + alice streak damping to pick bob, got %+v", mentioned)
	}
}

func TestGroupTurnParticipationWaitHandoffAndObservedByIsolation(t *testing.T) {
	var (
		mu              sync.Mutex
		partCalls       int
		handoffCalls    int
		capturedPrompts = make(map[string]string)
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "chat-1", "context_length": 32768}},
			})
		case "/v1/decisions":
			raw := string(body)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case strings.Contains(raw, `"participation"`):
				partCalls++
				choice := "act"
				// First candidate (Alice, faster) yields with "wait"; Bob acts; then Alice acts.
				if partCalls == 1 {
					choice = "wait"
				}
				probs := []map[string]any{
					{"value": "act", "probability": 0.9},
					{"value": "wait", "probability": 0.1},
				}
				if choice == "wait" {
					probs = []map[string]any{
						{"value": "act", "probability": 0.1},
						{"value": "wait", "probability": 0.9},
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"model": "dec-1",
					"usage": map[string]any{"input_tokens": 5, "output_tokens": 1},
					"answers": []map[string]any{
						{"name": "participation", "type": "choice", "choice": choice, "confidence": 0.9, "probabilities": probs},
					},
				})
			case strings.Contains(raw, `"handoff"`):
				handoffCalls++
				prob := 0.1
				if handoffCalls >= 2 {
					prob = 0.9
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"model": "dec-1",
					"usage": map[string]any{"input_tokens": 5, "output_tokens": 1},
					"answers": []map[string]any{
						{"name": "handoff", "type": "predicate", "probability": prob},
					},
				})
			default:
				var decReq struct {
					Questions []struct {
						Name string `json:"name"`
						Type string `json:"type"`
					} `json:"questions"`
				}
				_ = json.Unmarshal(body, &decReq)
				ans := make([]map[string]any, 0, len(decReq.Questions))
				for _, q := range decReq.Questions {
					if q.Type == "score" {
						ans = append(ans, map[string]any{"name": q.Name, "type": "score", "score": 0.0, "confidence": 0.9})
					} else {
						ans = append(ans, map[string]any{"name": q.Name, "type": "predicate", "probability": 0.1})
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"model":   "dec-1",
					"usage":   map[string]any{"input_tokens": 5, "output_tokens": 1},
					"answers": ans,
				})
			}
		case "/v1/chat/completions":
			var req struct {
				Stream   bool `json:"stream"`
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.Unmarshal(body, &req)
			if !req.Stream {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": `[]`}}},
				})
				return
			}
			promptAll := ""
			speaker := "unknown"
			for _, m := range req.Messages {
				promptAll += m.Content + "\n"
				if m.Role == "system" && strings.Contains(m.Content, "roleplaying as ") {
					for _, name := range []string{"Alice", "Bob", "Charlie"} {
						if strings.Contains(m.Content, "roleplaying as "+name) {
							speaker = name
						}
					}
				}
			}
			mu.Lock()
			capturedPrompts[speaker] = promptAll
			mu.Unlock()

			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			reply := speaker + " reporting in!"
			chunk, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": reply}}},
			})
			_, _ = w.Write([]byte("data: " + string(chunk) + "\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			if fl != nil {
				fl.Flush()
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	rdb, err := sqlite.Open(t.Context(), filepath.Join(dir, "q.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rdb.Close() }()
	idx, err := blevehnsw.Open(filepath.Join(dir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idx.Close() }()

	cfg := config.Default()
	cfg.LLM.Providers = []gateway.ProviderConfig{{
		ID:      "mock",
		Type:    "openai-compatible",
		Enabled: true,
		BaseURL: srv.URL + "/v1",
	}}
	cfg.LLM.Roles.ChatModel = "mock/chat-1"
	cfg.LLM.Roles.MemoryModel = "" // skip extra post-commit chat calls in test
	cfg.Decisions = decisions.Config{
		Providers:      []decisions.ProviderConfig{{ID: "d", Protocol: "openai", BaseURL: srv.URL + "/v1"}},
		Models:         []decisions.ModelConfig{{ID: "d/dec-1", ProviderID: "d", Model: "dec-1"}},
		DefaultModelID: "d/dec-1",
	}

	gw, err := gateway.NewContext(t.Context(), cfg.GatewayConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gw.Close() }()
	decReg, err := decisions.NewRegistry(cfg.Decisions, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	persona, _ := rdb.Personas().Create(ctx, domain.Persona{Name: "Alex"})
	alice, _ := rdb.Characters().Create(ctx, domain.Character{Name: "Alice", Personality: "Quick"})
	bob, _ := rdb.Characters().Create(ctx, domain.Character{Name: "Bob", Personality: "Steady"})
	charlie, _ := rdb.Characters().Create(ctx, domain.Character{Name: "Charlie", Personality: "Latecomer"})

	room, err := rdb.Rooms().Create(ctx, domain.Room{
		Title:            "Bridge",
		Mode:             domain.RoomModeGroup,
		PersonaID:        persona.ID,
		PersonaSnapshot:  persona,
		MaxTurnsPerInput: 4,
	}, []domain.RoomMember{
		{CharacterID: alice.ID, CharacterSnapshot: alice, BaseSpeed: 100, Active: true},
		{CharacterID: bob.ID, CharacterSnapshot: bob, BaseSpeed: 50, Active: true},
		{CharacterID: charlie.ID, CharacterSnapshot: charlie, BaseSpeed: 90, Active: false},
	})
	if err != nil {
		t.Fatal(err)
	}

	eng := runtime.NewEngine(rdb, idx, staticProviders{snap: runtime.ProviderSnapshot{
		Config:    cfg,
		Gateway:   gw,
		Decisions: decReg,
	}}, nil)
	defer eng.Close()

	eventsCh, cancel := eng.Events().Subscribe(room.ID, 0)
	defer cancel()

	if _, err := eng.SubmitTurn(ctx, room.ID, runtime.SubmitTurnRequest{
		RequestID: "req-g1",
		Text:      "Status report please",
	}); err != nil {
		t.Fatalf("SubmitTurn: %v", err)
	}
	waitForEvent(t, eventsCh, "turn.completed", 5*time.Second)

	msgs, err := rdb.Messages().List(ctx, room.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (user + bob + alice), got %d: %+v", len(msgs), msgs)
	}
	if msgs[1].AuthorName != "Bob" || msgs[2].AuthorName != "Alice" {
		t.Fatalf("expected speaker order Bob then Alice (after Alice waited once), got %s then %s", msgs[1].AuthorName, msgs[2].AuthorName)
	}
	for _, m := range msgs {
		if slices.Contains(m.ObservedBy, charlie.ID) {
			t.Fatalf("inactive member Charlie must not be in Turn 1 ObservedBy: %+v", m.ObservedBy)
		}
	}

	// Now deactivate Alice and Bob, activate Charlie, and verify Charlie does not see Turn 1 messages.
	_ = rdb.Rooms().UpsertMember(ctx, domain.RoomMember{RoomID: room.ID, CharacterID: alice.ID, CharacterSnapshot: alice, BaseSpeed: 100, Active: false})
	_ = rdb.Rooms().UpsertMember(ctx, domain.RoomMember{RoomID: room.ID, CharacterID: bob.ID, CharacterSnapshot: bob, BaseSpeed: 50, Active: false})
	_ = rdb.Rooms().UpsertMember(ctx, domain.RoomMember{RoomID: room.ID, CharacterID: charlie.ID, CharacterSnapshot: charlie, BaseSpeed: 90, Active: true})

	if _, err := eng.SubmitTurn(ctx, room.ID, runtime.SubmitTurnRequest{
		RequestID: "req-g2",
		Text:      "Charlie, what do you know?",
	}); err != nil {
		t.Fatalf("SubmitTurn 2: %v", err)
	}
	waitForEvent(t, eventsCh, "turn.completed", 5*time.Second)

	mu.Lock()
	charliePrompt := capturedPrompts["Charlie"]
	mu.Unlock()
	if strings.Contains(charliePrompt, "Bob reporting in!") || strings.Contains(charliePrompt, "Status report please") {
		t.Fatalf("cognitive isolation breach: Charlie's prompt saw Turn 1 content:\n%s", charliePrompt)
	}
	if !strings.Contains(charliePrompt, "Charlie, what do you know?") {
		t.Fatalf("expected Turn 2 message in Charlie's prompt, got:\n%s", charliePrompt)
	}
}

func TestGroupTurnMaxTurnsPerInputCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "chat-1", "context_length": 32768}},
			})
		case "/v1/decisions":
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), `"participation"`) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"model": "dec-1",
					"usage": map[string]any{"input_tokens": 5, "output_tokens": 1},
					"answers": []map[string]any{
						{
							"name": "participation", "type": "choice", "choice": "act", "confidence": 0.9,
							"probabilities": []map[string]any{{"value": "act", "probability": 0.9}, {"value": "wait", "probability": 0.1}},
						},
					},
				})
				return
			}
			if strings.Contains(string(body), `"handoff"`) {
				// Always say do NOT handoff -> loop must still stop at MaxTurnsPerInput.
				_ = json.NewEncoder(w).Encode(map[string]any{
					"model": "dec-1",
					"usage": map[string]any{"input_tokens": 5, "output_tokens": 1},
					"answers": []map[string]any{
						{"name": "handoff", "type": "predicate", "probability": 0.05},
					},
				})
				return
			}
			var decReq struct {
				Questions []struct {
					Name string `json:"name"`
					Type string `json:"type"`
				} `json:"questions"`
			}
			_ = json.Unmarshal(body, &decReq)
			ans := make([]map[string]any, 0, len(decReq.Questions))
			for _, q := range decReq.Questions {
				if q.Type == "score" {
					ans = append(ans, map[string]any{"name": q.Name, "type": "score", "score": 0.0, "confidence": 0.9})
				} else {
					ans = append(ans, map[string]any{"name": q.Name, "type": "predicate", "probability": 0.1})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model":   "dec-1",
				"usage":   map[string]any{"input_tokens": 5, "output_tokens": 1},
				"answers": ans,
			})
		case "/v1/chat/completions":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Stream bool `json:"stream"`
			}
			_ = json.Unmarshal(body, &req)
			if !req.Stream {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": `[]`}}},
				})
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Step reply\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	rdb, err := sqlite.Open(t.Context(), filepath.Join(dir, "q.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rdb.Close() }()
	idx, err := blevehnsw.Open(filepath.Join(dir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idx.Close() }()

	cfg := config.Default()
	cfg.LLM.Providers = []gateway.ProviderConfig{{ID: "mock", Type: "openai-compatible", Enabled: true, BaseURL: srv.URL + "/v1"}}
	cfg.LLM.Roles.ChatModel = "mock/chat-1"
	cfg.LLM.Roles.MemoryModel = ""
	cfg.Decisions = decisions.Config{
		Providers:      []decisions.ProviderConfig{{ID: "d", Protocol: "openai", BaseURL: srv.URL + "/v1"}},
		Models:         []decisions.ModelConfig{{ID: "d/dec-1", ProviderID: "d", Model: "dec-1"}},
		DefaultModelID: "d/dec-1",
	}
	gw, _ := gateway.NewContext(t.Context(), cfg.GatewayConfig())
	defer func() { _ = gw.Close() }()
	decReg, _ := decisions.NewRegistry(cfg.Decisions, nil)

	ctx := t.Context()
	persona, _ := rdb.Personas().Create(ctx, domain.Persona{Name: "Alex"})
	c1, _ := rdb.Characters().Create(ctx, domain.Character{Name: "A"})
	c2, _ := rdb.Characters().Create(ctx, domain.Character{Name: "B"})
	room, _ := rdb.Rooms().Create(ctx, domain.Room{
		Title:            "Cap",
		Mode:             domain.RoomModeGroup,
		PersonaID:        persona.ID,
		PersonaSnapshot:  persona,
		MaxTurnsPerInput: 2,
	}, []domain.RoomMember{
		{CharacterID: c1.ID, CharacterSnapshot: c1, BaseSpeed: 60, Active: true},
		{CharacterID: c2.ID, CharacterSnapshot: c2, BaseSpeed: 50, Active: true},
	})

	eng := runtime.NewEngine(rdb, idx, staticProviders{snap: runtime.ProviderSnapshot{
		Config:    cfg,
		Gateway:   gw,
		Decisions: decReg,
	}}, nil)
	defer eng.Close()

	eventsCh, cancel := eng.Events().Subscribe(room.ID, 0)
	defer cancel()

	if _, err := eng.SubmitTurn(ctx, room.ID, runtime.SubmitTurnRequest{
		RequestID: "req-cap",
		Text:      "Go",
	}); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, eventsCh, "turn.completed", 5*time.Second)

	msgs, _ := rdb.Messages().List(ctx, room.ID, 0, 20)
	charCount := 0
	for _, m := range msgs {
		if m.Role == domain.MessageRoleCharacter {
			charCount++
		}
	}
	if charCount != 2 {
		t.Fatalf("expected exactly 2 character messages due to MaxTurnsPerInput=2, got %d", charCount)
	}
}
