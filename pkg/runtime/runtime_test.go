package runtime_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/q/v2/pkg/app/config"
	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/llm/gateway"
	"github.com/snowmerak/q/v2/pkg/runtime"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/sqlite"
	"github.com/snowmerak/q/v2/pkg/storage/search/blevehnsw"
)

type staticProviders struct {
	snap runtime.ProviderSnapshot
}

func (s staticProviders) Snapshot() runtime.ProviderSnapshot { return s.snap }

func newRuntimeFixture(t *testing.T, upstream http.Handler, extraPrefs *domain.ModelPrefs) (*runtime.Engine, storage.RDBStore, string) {
	t.Helper()
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	rdb, err := sqlite.Open(t.Context(), filepath.Join(dir, "q.sqlite"))
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
	cfg.LLM.Providers = []gateway.ProviderConfig{{
		ID:      "mock",
		Type:    "openai-compatible",
		Enabled: true,
		BaseURL: srv.URL + "/v1",
	}}
	cfg.LLM.Roles.ChatModel = "mock/chat-1"
	cfg.LLM.Roles.MemoryModel = "mock/chat-1"

	gw, err := gateway.NewContext(t.Context(), cfg.GatewayConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gw.Close() })

	ctx := t.Context()
	persona, err := rdb.Personas().Create(ctx, domain.Persona{Name: "Alex", Description: "Traveler"})
	if err != nil {
		t.Fatal(err)
	}
	char, err := rdb.Characters().Create(ctx, domain.Character{
		Name:        "Noa",
		Personality: "Calm and observant",
		ModelPrefs:  extraPrefs,
	})
	if err != nil {
		t.Fatal(err)
	}
	room, err := rdb.Rooms().Create(ctx, domain.Room{
		Title:           "Station",
		Mode:            domain.RoomModeDirect,
		PersonaID:       persona.ID,
		PersonaSnapshot: persona,
	}, []domain.RoomMember{{
		CharacterID:       char.ID,
		CharacterSnapshot: char,
		Active:            true,
	}})
	if err != nil {
		t.Fatal(err)
	}

	eng := runtime.NewEngine(rdb, idx, staticProviders{snap: runtime.ProviderSnapshot{
		Config:  cfg,
		Gateway: gw,
	}}, nil)
	t.Cleanup(eng.Close)
	return eng, rdb, room.ID
}

func waitForEvent(t *testing.T, ch <-chan runtime.Event, target string, timeout time.Duration) runtime.Event {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("event channel closed before receiving %s", target)
			}
			if ev.Type == target {
				return ev
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for event %s", target)
		}
	}
}

func TestDirectStreamingIdempotencyAndReplay(t *testing.T) {
	eng, rdb, roomID := newRuntimeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"chat-1"}]}`)
		case "/v1/chat/completions":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Stream bool `json:"stream"`
			}
			_ = json.Unmarshal(body, &req)
			if req.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"chat-1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Tea is \"}}]}\n\n")
				_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"chat-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ready.\"},\"finish_reason\":\"stop\"}]}\n\n")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"r1","object":"chat.completion","model":"chat-1","choices":[{"index":0,"message":{"role":"assistant","content":"[]"},"finish_reason":"stop"}]}`)
		}
	}), nil)

	ch, unsub := eng.Events().Subscribe(roomID, 0)
	defer unsub()

	res, err := eng.SubmitTurn(t.Context(), roomID, runtime.SubmitTurnRequest{
		RequestID: "req-1",
		Text:      "Any tea left?",
	})
	if err != nil {
		t.Fatalf("SubmitTurn: %v", err)
	}

	var sawDeltaID string
	var sawCompletedID string
	var lastEventID int64
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			lastEventID = ev.ID
			switch ev.Type {
			case "bubble.delta":
				m := ev.Data.(map[string]any)
				sawDeltaID, _ = m["message_id"].(string)
			case "bubble.completed":
				m := ev.Data.(map[string]any)
				msg := m["message"].(domain.Message)
				sawCompletedID = msg.ID
			case "turn.completed":
				goto DONE
			case "turn.failed":
				t.Fatalf("unexpected turn.failed: %#v", ev.Data)
			}
		case <-timeout:
			t.Fatal("timed out waiting for turn.completed")
		}
	}
DONE:
	if sawDeltaID == "" || sawDeltaID != sawCompletedID {
		t.Fatalf("delta message_id=%q != completed message.id=%q", sawDeltaID, sawCompletedID)
	}

	msgs, err := rdb.Messages().List(t.Context(), roomID, 0, 10)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("expected 2 committed messages, got %d (err=%v)", len(msgs), err)
	}
	if msgs[1].Content != "Tea is ready." {
		t.Fatalf("reply content = %q", msgs[1].Content)
	}

	// Idempotent replay with same request_id + same text.
	replay, err := eng.SubmitTurn(t.Context(), roomID, runtime.SubmitTurnRequest{
		RequestID: "req-1",
		Text:      "Any tea left?",
	})
	if err != nil || !replay.AlreadyCompleted || replay.Turn.ID != res.Turn.ID {
		t.Fatalf("expected AlreadyCompleted replay, got %+v err=%v", replay, err)
	}

	// Same request_id with different text -> ErrIdempotencyConflict.
	if _, err := eng.SubmitTurn(t.Context(), roomID, runtime.SubmitTurnRequest{
		RequestID: "req-1",
		Text:      "Actually coffee.",
	}); !errors.Is(err, runtime.ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
	}

	// Last-Event-ID replay from 1 returns newer events immediately.
	replayCh, unsubReplay := eng.Events().Subscribe(roomID, 1)
	defer unsubReplay()
	var replayed []int64
	for len(replayCh) > 0 {
		ev := <-replayCh
		replayed = append(replayed, ev.ID)
	}
	if len(replayed) == 0 || replayed[0] != 2 || replayed[len(replayed)-1] != lastEventID {
		t.Fatalf("unexpected Last-Event-ID replay IDs: %v (last=%d)", replayed, lastEventID)
	}
}

func TestTurnRetryFromCheckpointAndCompaction(t *testing.T) {
	var failFirst atomic.Bool
	failFirst.Store(true)
	var compactionSummaryCalled atomic.Bool

	eng, rdb, roomID := newRuntimeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"chat-1"}]}`)
		case "/v1/chat/completions":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Stream   bool `json:"stream"`
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.Unmarshal(body, &req)
			if !req.Stream && len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "running memory summary") {
				compactionSummaryCalled.Store(true)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"sum1","object":"chat.completion","model":"chat-1","choices":[{"index":0,"message":{"role":"assistant","content":"Compacted summary of earlier turns."},"finish_reason":"stop"}]}`)
				return
			}
			if !req.Stream && len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "value/principle candidates") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"val1","object":"chat.completion","model":"chat-1","choices":[{"index":0,"message":{"role":"assistant","content":"[\"Always verify coordinates twice.\"]"},"finish_reason":"stop"}]}`)
				return
			}
			if !req.Stream {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"r1","object":"chat.completion","model":"chat-1","choices":[{"index":0,"message":{"role":"assistant","content":"[]"},"finish_reason":"stop"}]}`)
				return
			}
			if failFirst.Swap(false) {
				http.Error(w, "temporary failure", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"c2\",\"object\":\"chat.completion.chunk\",\"model\":\"chat-1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Recovered cleanly.\"},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}
	}), &domain.ModelPrefs{ContextWindowOverride: 400})

	// Seed long prior messages so input exceeds 85% of 400 tokens on retry.
	members, _ := rdb.Rooms().ListMembers(t.Context(), roomID)
	actorID := members[0].CharacterID
	room, _ := rdb.Rooms().Get(t.Context(), roomID)
	seedTurn, _, err := rdb.Turns().CreateOrGetIdempotent(t.Context(), domain.Turn{
		RoomID:    roomID,
		RequestID: "seed-req",
		InputHash: "seed-hash",
		Status:    domain.TurnStatusCompleted,
	})
	if err != nil {
		t.Fatal(err)
	}
	longChunk := strings.Repeat("northern lighthouse signal log entry ", 16)
	for range 3 {
		if _, err := rdb.Messages().Append(t.Context(), domain.Message{
			RoomID:     roomID,
			TurnID:     seedTurn.ID,
			Role:       domain.MessageRoleUser,
			AuthorID:   room.PersonaID,
			AuthorName: "Alex",
			Content:    longChunk,
			ObservedBy: []string{room.PersonaID, actorID},
		}); err != nil {
			t.Fatal(err)
		}
	}

	ch, unsub := eng.Events().Subscribe(roomID, 0)
	defer unsub()

	res, err := eng.SubmitTurn(t.Context(), roomID, runtime.SubmitTurnRequest{
		RequestID: "req-fail-retry",
		Text:      "Check the lighthouse log.",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, ch, "turn.failed", 5*time.Second)

	if _, err := eng.RetryTurn(t.Context(), roomID, res.Turn.ID); err != nil {
		t.Fatalf("RetryTurn: %v", err)
	}
	waitForEvent(t, ch, "turn.completed", 5*time.Second)

	if !compactionSummaryCalled.Load() {
		t.Fatal("expected 85% budget compaction to summarize older messages")
	}
	updatedMembers, _ := rdb.Rooms().ListMembers(t.Context(), roomID)
	if updatedMembers[0].ActorState.CompressedMemo == "" || updatedMembers[0].ActorState.CompactedThroughSeq == 0 {
		t.Fatalf("expected updated CompressedMemo and CompactedThroughSeq, got %+v", updatedMembers[0].ActorState)
	}
	job, err := rdb.Memories().ClaimNextValueJob(t.Context(), domain.NowMS()+10000)
	if err != nil || job == nil {
		t.Fatalf("expected queued value_job from compaction, job=%v err=%v", job, err)
	}
}

func TestInterceptStopAndCutIn(t *testing.T) {
	firstStarted := make(chan struct{}, 2)
	secondPromptSawCutIn := make(chan bool, 1)
	var callNum atomic.Int32

	eng, rdb, roomID := newRuntimeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"chat-1"}]}`)
		case "/v1/chat/completions":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Stream bool `json:"stream"`
			}
			_ = json.Unmarshal(body, &req)
			if !req.Stream {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"r1","object":"chat.completion","model":"chat-1","choices":[{"index":0,"message":{"role":"assistant","content":"[]"},"finish_reason":"stop"}]}`)
				return
			}
			n := callNum.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			if n == 1 || n == 2 {
				_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"chat-1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial...\"}}]}\n\n")
				flusher.Flush()
				firstStarted <- struct{}{}
				<-r.Context().Done()
				return
			}
			secondPromptSawCutIn <- strings.Contains(string(body), "Wait, bring umbrella instead!")
			_, _ = io.WriteString(w, "data: {\"id\":\"c2\",\"object\":\"chat.completion.chunk\",\"model\":\"chat-1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Got it, umbrella packed.\"},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}
	}), nil)

	ch, unsub := eng.Events().Subscribe(roomID, 0)
	defer unsub()

	// Test 1: Stop discards uncommitted partial bubble.
	resStop, err := eng.SubmitTurn(t.Context(), roomID, runtime.SubmitTurnRequest{
		RequestID: "req-stop",
		Text:      "Pack our bags.",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-firstStarted
	if !eng.Intercept(roomID, "") {
		t.Fatal("expected Intercept stop accepted")
	}
	evStop := waitForEvent(t, ch, "turn.completed", 5*time.Second)
	if evStop.Data.(map[string]any)["status"] != "cancelled" {
		t.Fatalf("expected cancelled turn.completed, got %#v", evStop.Data)
	}
	turnStop, _ := rdb.Turns().Get(t.Context(), resStop.Turn.ID)
	if turnStop.Status != domain.TurnStatusCancelled {
		t.Fatalf("turn status = %s, want cancelled", turnStop.Status)
	}
	msgsAfterStop, _ := rdb.Messages().List(t.Context(), roomID, 0, 10)
	if len(msgsAfterStop) != 1 {
		t.Fatalf("expected only user message committed after stop, got %d", len(msgsAfterStop))
	}

	// Test 2: Cut-in discards uncommitted bubble, commits cut-in user message, and replies to cut-in.
	if _, err := eng.SubmitTurn(t.Context(), roomID, runtime.SubmitTurnRequest{
		RequestID: "req-cutin",
		Text:      "What are we bringing?",
	}); err != nil {
		t.Fatal(err)
	}
	<-firstStarted
	if !eng.Intercept(roomID, "Wait, bring umbrella instead!") {
		t.Fatal("expected Intercept cut_in accepted")
	}
	evDone := waitForEvent(t, ch, "turn.completed", 5*time.Second)
	if evDone.Data.(map[string]any)["status"] != "completed" {
		t.Fatalf("expected completed after cut_in, got %#v", evDone.Data)
	}
	if !<-secondPromptSawCutIn {
		t.Fatal("expected restarted generation prompt to contain cut-in message")
	}
	msgsFinal, _ := rdb.Messages().List(t.Context(), roomID, 0, 20)
	// Previous user(1) + req-cutin user(1) + cut_in user(1) + assistant(1) = 4
	if len(msgsFinal) != 4 || msgsFinal[3].Content != "Got it, umbrella packed." {
		t.Fatalf("unexpected final messages: %+v", msgsFinal)
	}
}
