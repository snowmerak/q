package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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

func TestGroupTurnAtomicCheckpointCommitAndOrphanRecovery(t *testing.T) {
	t.Run("max_turns_1_completes_atomically", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/models":
				_, _ = io.WriteString(w, `{"data":[{"id":"chat-1","context_length":32768}]}`)
			case "/v1/decisions":
				body, _ := io.ReadAll(r.Body)
				raw := string(body)
				switch {
				case strings.Contains(raw, `"participation"`):
					_ = json.NewEncoder(w).Encode(map[string]any{
						"model":   "dec-1",
						"usage":   map[string]any{"input_tokens": 5, "output_tokens": 1},
						"answers": []map[string]any{{"name": "participation", "type": "choice", "choice": "act", "confidence": 0.9, "probabilities": []map[string]any{{"value": "act", "probability": 0.9}, {"value": "wait", "probability": 0.1}}}},
					})
				case strings.Contains(raw, `"handoff"`):
					_ = json.NewEncoder(w).Encode(map[string]any{
						"model":   "dec-1",
						"usage":   map[string]any{"input_tokens": 5, "output_tokens": 1},
						"answers": []map[string]any{{"name": "handoff", "type": "predicate", "probability": 0.05}},
					})
				default:
					_ = json.NewEncoder(w).Encode(map[string]any{
						"model":   "dec-1",
						"usage":   map[string]any{"input_tokens": 5, "output_tokens": 1},
						"answers": []map[string]any{},
					})
				}
			case "/v1/chat/completions":
				body, _ := io.ReadAll(r.Body)
				var req struct {
					Stream bool `json:"stream"`
				}
				_ = json.Unmarshal(body, &req)
				if !req.Stream {
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": `[]`}}}})
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Single group reply.\"}}]}\n\n"))
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
			Title:            "Cap1",
			Mode:             domain.RoomModeGroup,
			PersonaID:        persona.ID,
			PersonaSnapshot:  persona,
			MaxTurnsPerInput: 1,
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

		ch, cancel := eng.Events().Subscribe(room.ID, 0)
		defer cancel()

		sub, err := eng.SubmitTurn(ctx, room.ID, runtime.SubmitTurnRequest{RequestID: "req-cap1", Text: "Go once"})
		if err != nil {
			t.Fatal(err)
		}
		waitForEvent(t, ch, "turn.completed", 5*time.Second)

		stored, err := rdb.Turns().Get(ctx, sub.Turn.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Status != domain.TurnStatusCompleted {
			t.Fatalf("expected completed turn, got %s", stored.Status)
		}
		var cp map[string]any
		if err := json.Unmarshal([]byte(stored.CheckpointJSON), &cp); err != nil {
			t.Fatal(err)
		}
		if cp["group_steps"] != float64(1) {
			t.Fatalf("expected checkpoint group_steps=1, got %v", cp["group_steps"])
		}
		if actor, _ := cp["actor_id"].(string); actor != "" {
			t.Fatalf("expected cleared checkpoint actor_id, got %q", actor)
		}
		if err := eng.RecoverOrphanTurns(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := eng.RetryTurn(ctx, room.ID, sub.Turn.ID); !errors.Is(err, runtime.ErrTurnNotRetryable) {
			t.Fatalf("expected ErrTurnNotRetryable on completed turn, got %v", err)
		}
		msgs, _ := rdb.Messages().List(ctx, room.ID, 0, 20)
		charCount := 0
		for _, m := range msgs {
			if m.Role == domain.MessageRoleCharacter {
				charCount++
			}
		}
		if charCount != 1 {
			t.Fatalf("expected exactly 1 character message, got %d", charCount)
		}
	})

	t.Run("max_turns_2_orphan_recovery_after_step_1", func(t *testing.T) {
		var handoffCalls atomic.Int32
		handoffEntered := make(chan struct{})
		stepCount := atomic.Int32{}

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/models":
				_, _ = io.WriteString(w, `{"data":[{"id":"chat-1","context_length":32768}]}`)
			case "/v1/decisions":
				body, _ := io.ReadAll(r.Body)
				raw := string(body)
				switch {
				case strings.Contains(raw, `"participation"`):
					_ = json.NewEncoder(w).Encode(map[string]any{
						"model":   "dec-1",
						"usage":   map[string]any{"input_tokens": 5, "output_tokens": 1},
						"answers": []map[string]any{{"name": "participation", "type": "choice", "choice": "act", "confidence": 0.9, "probabilities": []map[string]any{{"value": "act", "probability": 0.9}, {"value": "wait", "probability": 0.1}}}},
					})
				case strings.Contains(raw, `"handoff"`):
					if handoffCalls.Add(1) == 1 {
						close(handoffEntered)
						<-r.Context().Done()
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"model":   "dec-1",
						"usage":   map[string]any{"input_tokens": 5, "output_tokens": 1},
						"answers": []map[string]any{{"name": "handoff", "type": "predicate", "probability": 0.05}},
					})
				default:
					_ = json.NewEncoder(w).Encode(map[string]any{
						"model":   "dec-1",
						"usage":   map[string]any{"input_tokens": 5, "output_tokens": 1},
						"answers": []map[string]any{},
					})
				}
			case "/v1/chat/completions":
				body, _ := io.ReadAll(r.Body)
				var req struct {
					Stream bool `json:"stream"`
				}
				_ = json.Unmarshal(body, &req)
				if !req.Stream {
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": `[]`}}}})
					return
				}
				n := stepCount.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Group step " + string(rune('0'+n)) + "\"}}]}\n\n"))
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
			Title:            "Cap2",
			Mode:             domain.RoomModeGroup,
			PersonaID:        persona.ID,
			PersonaSnapshot:  persona,
			MaxTurnsPerInput: 2,
		}, []domain.RoomMember{
			{CharacterID: c1.ID, CharacterSnapshot: c1, BaseSpeed: 60, Active: true},
			{CharacterID: c2.ID, CharacterSnapshot: c2, BaseSpeed: 50, Active: true},
		})

		eng1 := runtime.NewEngine(rdb, idx, staticProviders{snap: runtime.ProviderSnapshot{
			Config:    cfg,
			Gateway:   gw,
			Decisions: decReg,
		}}, nil)
		ch1, cancel1 := eng1.Events().Subscribe(room.ID, 0)
		sub, err := eng1.SubmitTurn(ctx, room.ID, runtime.SubmitTurnRequest{RequestID: "req-cap2", Text: "Two steps"})
		if err != nil {
			t.Fatal(err)
		}
		waitForEvent(t, ch1, "bubble.completed", 5*time.Second)
		select {
		case <-handoffEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for handoff evaluation after step 1")
		}

		// Before step 2 runs, the DB turn row must ALREADY carry group_steps=1 atomically with bubble 1.
		midTurn, err := rdb.Turns().Get(ctx, sub.Turn.ID)
		if err != nil {
			t.Fatal(err)
		}
		if midTurn.Status != domain.TurnStatusRunning {
			t.Fatalf("expected running turn mid-group, got %s", midTurn.Status)
		}
		var midCP map[string]any
		if err := json.Unmarshal([]byte(midTurn.CheckpointJSON), &midCP); err != nil {
			t.Fatal(err)
		}
		if midCP["group_steps"] != float64(1) {
			t.Fatalf("expected atomic DB checkpoint group_steps=1 right after bubble 1, got %v", midCP["group_steps"])
		}
		if actor, _ := midCP["actor_id"].(string); actor != "" {
			t.Fatalf("expected cleared actor_id in DB checkpoint after bubble 1, got %q", actor)
		}

		cancel1()
		eng1.Close()

		// Simulate ungraceful crash leaving the turn in running status right after step 1's checkpoint.
		midTurn.Status = domain.TurnStatusRunning
		midTurn.CompletedAt = nil
		if err := rdb.Turns().Update(context.Background(), midTurn); err != nil {
			t.Fatal(err)
		}

		eng2 := runtime.NewEngine(rdb, idx, staticProviders{snap: runtime.ProviderSnapshot{
			Config:    cfg,
			Gateway:   gw,
			Decisions: decReg,
		}}, nil)
		defer eng2.Close()

		if err := eng2.RecoverOrphanTurns(ctx); err != nil {
			t.Fatalf("RecoverOrphanTurns: %v", err)
		}
		recovered, err := rdb.Turns().Get(ctx, sub.Turn.ID)
		if err != nil {
			t.Fatal(err)
		}
		if recovered.Status != domain.TurnStatusFailed || recovered.ErrorCode != "interrupted" {
			t.Fatalf("expected interrupted failed turn after orphan recovery, got %+v", recovered)
		}

		ch2, cancel2 := eng2.Events().Subscribe(room.ID, 0)
		defer cancel2()
		if _, err := eng2.RetryTurn(ctx, room.ID, sub.Turn.ID); err != nil {
			t.Fatalf("RetryTurn: %v", err)
		}
		waitForEvent(t, ch2, "turn.completed", 5*time.Second)

		msgs, _ := rdb.Messages().List(ctx, room.ID, 0, 20)
		charCount := 0
		for _, m := range msgs {
			if m.Role == domain.MessageRoleCharacter {
				charCount++
			}
		}
		if charCount != 2 {
			t.Fatalf("expected exactly 2 character messages after crash recovery (not 3), got %d", charCount)
		}
	})

	t.Run("max_turns_2_orphan_recovery_honors_handoff_to_user", func(t *testing.T) {
		var handoffCalls atomic.Int32
		handoffEntered := make(chan struct{})
		stepCount := atomic.Int32{}

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/models":
				_, _ = io.WriteString(w, `{"data":[{"id":"chat-1","context_length":32768}]}`)
			case "/v1/decisions":
				body, _ := io.ReadAll(r.Body)
				raw := string(body)
				switch {
				case strings.Contains(raw, `"participation"`):
					_ = json.NewEncoder(w).Encode(map[string]any{
						"model":   "dec-1",
						"usage":   map[string]any{"input_tokens": 5, "output_tokens": 1},
						"answers": []map[string]any{{"name": "participation", "type": "choice", "choice": "act", "confidence": 0.9, "probabilities": []map[string]any{{"value": "act", "probability": 0.9}, {"value": "wait", "probability": 0.1}}}},
					})
				case strings.Contains(raw, `"handoff"`):
					callN := handoffCalls.Add(1)
					if callN == 1 {
						close(handoffEntered)
						<-r.Context().Done()
						return
					}
					// On retry recovery, decision evaluates handoff=true -> yield to user immediately.
					_ = json.NewEncoder(w).Encode(map[string]any{
						"model":   "dec-1",
						"usage":   map[string]any{"input_tokens": 5, "output_tokens": 1},
						"answers": []map[string]any{{"name": "handoff", "type": "predicate", "probability": 0.95}},
					})
				default:
					_ = json.NewEncoder(w).Encode(map[string]any{
						"model":   "dec-1",
						"usage":   map[string]any{"input_tokens": 5, "output_tokens": 1},
						"answers": []map[string]any{},
					})
				}
			case "/v1/chat/completions":
				body, _ := io.ReadAll(r.Body)
				var req struct {
					Stream bool `json:"stream"`
				}
				_ = json.Unmarshal(body, &req)
				if !req.Stream {
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": `[]`}}}})
					return
				}
				n := stepCount.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Handoff check step " + string(rune('0'+n)) + "\"}}]}\n\n"))
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
			Title:            "Cap2Handoff",
			Mode:             domain.RoomModeGroup,
			PersonaID:        persona.ID,
			PersonaSnapshot:  persona,
			MaxTurnsPerInput: 2,
		}, []domain.RoomMember{
			{CharacterID: c1.ID, CharacterSnapshot: c1, BaseSpeed: 60, Active: true},
			{CharacterID: c2.ID, CharacterSnapshot: c2, BaseSpeed: 50, Active: true},
		})

		eng1 := runtime.NewEngine(rdb, idx, staticProviders{snap: runtime.ProviderSnapshot{
			Config:    cfg,
			Gateway:   gw,
			Decisions: decReg,
		}}, nil)
		ch1, cancel1 := eng1.Events().Subscribe(room.ID, 0)
		sub, err := eng1.SubmitTurn(ctx, room.ID, runtime.SubmitTurnRequest{RequestID: "req-cap2-handoff", Text: "Stop if satisfied"})
		if err != nil {
			t.Fatal(err)
		}
		waitForEvent(t, ch1, "bubble.completed", 5*time.Second)
		select {
		case <-handoffEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for handoff evaluation after step 1")
		}

		midTurn, err := rdb.Turns().Get(ctx, sub.Turn.ID)
		if err != nil {
			t.Fatal(err)
		}
		cancel1()
		eng1.Close()

		midTurn.Status = domain.TurnStatusRunning
		midTurn.CompletedAt = nil
		if err := rdb.Turns().Update(context.Background(), midTurn); err != nil {
			t.Fatal(err)
		}

		eng2 := runtime.NewEngine(rdb, idx, staticProviders{snap: runtime.ProviderSnapshot{
			Config:    cfg,
			Gateway:   gw,
			Decisions: decReg,
		}}, nil)
		defer eng2.Close()

		if err := eng2.RecoverOrphanTurns(ctx); err != nil {
			t.Fatalf("RecoverOrphanTurns: %v", err)
		}
		ch2, cancel2 := eng2.Events().Subscribe(room.ID, 0)
		defer cancel2()
		if _, err := eng2.RetryTurn(ctx, room.ID, sub.Turn.ID); err != nil {
			t.Fatalf("RetryTurn: %v", err)
		}
		waitForEvent(t, ch2, "turn.completed", 5*time.Second)

		if gotSteps := stepCount.Load(); gotSteps != 1 {
			t.Fatalf("expected only 1 actor generation (recovery must honor handoff=true instead of speaking again), got %d", gotSteps)
		}
		msgs, _ := rdb.Messages().List(ctx, room.ID, 0, 20)
		charCount := 0
		for _, m := range msgs {
			if m.Role == domain.MessageRoleCharacter {
				charCount++
			}
		}
		if charCount != 1 {
			t.Fatalf("expected 1 character message after handoff=true on recovery, got %d", charCount)
		}
	})
}

func TestGroupTurnRetryAfterLaterCompletedTurnUsesOwnReplyForHandoff(t *testing.T) {
	const firstReply = "Turn1-Alpha"
	const laterReply = "Turn2-Beta"
	handoffEntered := make(chan struct{})
	var retrying, retryHandoffSeen atomic.Bool
	var generations atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"chat-1","context_length":32768}]}`)
		case "/v1/decisions":
			body, _ := io.ReadAll(r.Body)
			raw := string(body)
			switch {
			case strings.Contains(raw, `"participation"`):
				// Both characters wait after Turn 2's reply, letting it finish with one step.
				choice, actProbability := "act", 0.9
				if !retrying.Load() && strings.Contains(raw, laterReply) {
					choice, actProbability = "wait", 0.1
				}
				_, _ = fmt.Fprintf(w, `{"model":"dec-1","usage":{"input_tokens":5,"output_tokens":1},"answers":[{"name":"participation","type":"choice","choice":%q,"confidence":0.9,"probabilities":[{"value":"act","probability":%.1f},{"value":"wait","probability":%.1f}]}]}`, choice, actProbability, 1-actProbability)
			case strings.Contains(raw, `"handoff"`):
				// The Decisions endpoint receives state as JSON encoded in the input string.
				var req struct {
					Input string `json:"input"`
				}
				var state struct {
					LastActorName string `json:"last_actor_name"`
					LastReply     string `json:"last_reply"`
				}
				if err := json.Unmarshal(body, &req); err != nil {
					t.Errorf("decode decision request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				if err := json.Unmarshal([]byte(req.Input), &state); err != nil {
					t.Errorf("decode handoff state: %v", err)
					http.Error(w, "invalid state", http.StatusBadRequest)
					return
				}
				if !retrying.Load() && state.LastReply == firstReply {
					close(handoffEntered)
					<-r.Context().Done()
					return
				}
				if retrying.Load() {
					retryHandoffSeen.Store(true)
					if state.LastActorName != "A" || state.LastReply != firstReply {
						t.Errorf("retry handoff got actor %q and reply %q, want A and %q", state.LastActorName, state.LastReply, firstReply)
					}
					if strings.Contains(raw, laterReply) {
						t.Errorf("retry handoff request contains later reply %q", laterReply)
					}
				}
				probability := 0.05
				if state.LastReply == firstReply {
					probability = 0.95
				}
				_, _ = fmt.Fprintf(w, `{"model":"dec-1","usage":{"input_tokens":5,"output_tokens":1},"answers":[{"name":"handoff","type":"predicate","probability":%.2f}]}`, probability)
			default:
				_, _ = io.WriteString(w, `{"model":"dec-1","usage":{"input_tokens":5,"output_tokens":1},"answers":[]}`)
			}
		case "/v1/chat/completions":
			var req struct {
				Stream bool `json:"stream"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if !req.Stream {
				_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"[]"}}]}`)
				return
			}
			reply := "Unexpected extra reply"
			switch generations.Add(1) {
			case 1:
				reply = firstReply
			case 2:
				reply = laterReply
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\ndata: [DONE]\n\n", reply)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	ctx := t.Context()
	dir := t.TempDir()
	rdb, err := sqlite.Open(ctx, filepath.Join(dir, "q.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	idx, err := blevehnsw.Open(filepath.Join(dir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	cfg := config.Default()
	cfg.LLM.Providers = []gateway.ProviderConfig{{ID: "mock", Type: "openai-compatible", Enabled: true, BaseURL: srv.URL + "/v1"}}
	cfg.LLM.Roles.ChatModel = "mock/chat-1"
	cfg.LLM.Roles.MemoryModel = ""
	cfg.Decisions = decisions.Config{
		Providers:      []decisions.ProviderConfig{{ID: "d", Protocol: "openai", BaseURL: srv.URL + "/v1"}},
		Models:         []decisions.ModelConfig{{ID: "d/dec-1", ProviderID: "d", Model: "dec-1"}},
		DefaultModelID: "d/dec-1",
	}
	gw, err := gateway.NewContext(ctx, cfg.GatewayConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gw.Close() })
	decReg, err := decisions.NewRegistry(cfg.Decisions, nil)
	if err != nil {
		t.Fatal(err)
	}
	providers := staticProviders{snap: runtime.ProviderSnapshot{Config: cfg, Gateway: gw, Decisions: decReg}}

	persona, err := rdb.Personas().Create(ctx, domain.Persona{Name: "Alex"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := rdb.Characters().Create(ctx, domain.Character{Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := rdb.Characters().Create(ctx, domain.Character{Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	room, err := rdb.Rooms().Create(ctx, domain.Room{
		Title:            "Group retry",
		Mode:             domain.RoomModeGroup,
		PersonaID:        persona.ID,
		PersonaSnapshot:  persona,
		MaxTurnsPerInput: 2,
	}, []domain.RoomMember{
		{CharacterID: a.ID, CharacterSnapshot: a, BaseSpeed: 60, Active: true},
		{CharacterID: b.ID, CharacterSnapshot: b, BaseSpeed: 50, Active: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	getTurn := func(id string) domain.Turn {
		t.Helper()
		turn, err := rdb.Turns().Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return turn
	}

	eng1 := runtime.NewEngine(rdb, idx, providers, nil)
	t.Cleanup(eng1.Close)
	first, err := eng1.SubmitTurn(ctx, room.ID, runtime.SubmitTurnRequest{RequestID: "turn-1", Text: "Go once"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-handoffEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Turn 1's handoff")
	}
	orphan := getTurn(first.Turn.ID)
	var cp struct {
		Phase      string `json:"phase"`
		GroupSteps int    `json:"group_steps"`
	}
	if err := json.Unmarshal([]byte(orphan.CheckpointJSON), &cp); err != nil {
		t.Fatal(err)
	}
	if orphan.Status != domain.TurnStatusRunning || cp.Phase != "group_step_committed" || cp.GroupSteps != 1 {
		t.Fatalf("expected running Turn 1 with one committed step, got status=%s checkpoint=%+v", orphan.Status, cp)
	}
	eng1.Close()
	// Restore the running checkpoint that an ungraceful exit would leave behind.
	if err := rdb.Turns().Update(ctx, orphan); err != nil {
		t.Fatal(err)
	}

	eng2 := runtime.NewEngine(rdb, idx, providers, nil)
	t.Cleanup(eng2.Close)
	if err := eng2.RecoverOrphanTurns(ctx); err != nil {
		t.Fatal(err)
	}
	if recovered := getTurn(orphan.ID); recovered.Status != domain.TurnStatusFailed || recovered.ErrorCode != "interrupted" {
		t.Fatalf("expected interrupted Turn 1, got status=%s error=%s", recovered.Status, recovered.ErrorCode)
	}
	ch, unsubscribe := eng2.Events().Subscribe(room.ID, 0)
	defer unsubscribe()
	later, err := eng2.SubmitTurn(ctx, room.ID, runtime.SubmitTurnRequest{RequestID: "turn-2", Text: "B"})
	if err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, ch, "turn.completed", 5*time.Second)
	if status := getTurn(later.Turn.ID).Status; status != domain.TurnStatusCompleted {
		t.Fatalf("expected completed Turn 2, got %s", status)
	}

	retrying.Store(true)
	if _, err := eng2.RetryTurn(ctx, room.ID, orphan.ID); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, ch, "turn.completed", 5*time.Second)
	if status := getTurn(orphan.ID).Status; status != domain.TurnStatusCompleted || !retryHandoffSeen.Load() {
		t.Fatalf("retry status=%s handoff evaluated=%t", status, retryHandoffSeen.Load())
	}

	msgs, err := rdb.Messages().List(ctx, room.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	replies := make(map[string][]string)
	for _, msg := range msgs {
		if msg.Role == domain.MessageRoleCharacter {
			replies[msg.TurnID] = append(replies[msg.TurnID], msg.Content)
		}
	}
	if !slices.Equal(replies[orphan.ID], []string{firstReply}) {
		t.Errorf("Turn 1 replies = %q, want only %q", replies[orphan.ID], firstReply)
	}
	if !slices.Equal(replies[later.Turn.ID], []string{laterReply}) {
		t.Errorf("Turn 2 replies = %q, want only %q", replies[later.Turn.ID], laterReply)
	}
}
