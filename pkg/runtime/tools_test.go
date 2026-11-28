package runtime_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
	"github.com/snowmerak/q/v2/pkg/storage/blob/localfs"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/sqlite"
	"github.com/snowmerak/q/v2/pkg/storage/search/blevehnsw"
	"github.com/snowmerak/q/v2/pkg/task"
)

func setupToolRuntime(
	t *testing.T,
	mode domain.ApprovalMode,
	upstream http.Handler,
) (*runtime.Engine, string, string, string) {
	t.Helper()
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	workDir := filepath.Join(dir, "workspace")
	_ = os.MkdirAll(workDir, 0o755)
	_ = os.WriteFile(filepath.Join(workDir, "hello.txt"), []byte("hello from workspace"), 0o644)

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

	blobs, err := localfs.Open(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blobs.Close() })

	cfg := config.Default()
	cfg.Tools.ApprovalMode = mode
	cfg.Tools.ToolOverrides = nil
	cfg.Tools.AllowedRoots = []string{workDir}
	cfg.LLM.Providers = []gateway.ProviderConfig{{
		ID: "mock", Type: "openai-compatible", Enabled: true, BaseURL: srv.URL + "/v1",
	}}
	cfg.LLM.Roles.ChatModel = "mock/chat-1"
	cfg.LLM.Roles.MemoryModel = "mock/chat-1"

	gw, err := gateway.NewContext(t.Context(), cfg.GatewayConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gw.Close() })

	persona, _ := rdb.Personas().Create(t.Context(), domain.Persona{Name: "Alex"})
	char, _ := rdb.Characters().Create(t.Context(), domain.Character{Name: "Noa"})
	room, _ := rdb.Rooms().Create(t.Context(), domain.Room{
		Title: "Workshop", Mode: domain.RoomModeDirect, PersonaID: persona.ID, PersonaSnapshot: persona,
	}, []domain.RoomMember{{
		CharacterID: char.ID, CharacterSnapshot: char, Active: true,
	}})

	reg := task.NewRegistry(task.RegistryDeps{
		RDB:    rdb,
		Search: idx,
		Loom:   task.NewLoomStore(rdb, blobs, 64<<20),
	})
	eng := runtime.NewEngine(rdb, idx, staticProviders{snap: runtime.ProviderSnapshot{
		Config:  cfg,
		Gateway: gw,
	}}, nil)
	eng.SetTools(reg, dir)
	t.Cleanup(eng.Close)

	return eng, room.ID, workDir, dir
}

func TestToolLoopBypassRequestApproveRejectAndAskUser(t *testing.T) {
	// 1. Bypass mode runs read_file and finishes with final bubble.
	var step atomic.Int32
	engBypass, roomBypass, _, _ := setupToolRuntime(t, domain.ApprovalBypass, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"chat-1"}]}`)
		case "/v1/chat/completions":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"stream":true`) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"r","object":"chat.completion","model":"chat-1","choices":[{"index":0,"message":{"role":"assistant","content":"[]"},"finish_reason":"stop"}]}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			if step.Add(1) == 1 {
				_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"hello.txt\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
				return
			}
			if !strings.Contains(string(body), "hello from workspace") {
				t.Fatalf("expected tool result in second round prompt: %s", string(body))
			}
			_, _ = io.WriteString(w, `data: {"id":"c2","object":"chat.completion.chunk","model":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","content":"Read hello from workspace cleanly."},"finish_reason":"stop"}]}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}
	}))

	chBypass, unsubBypass := engBypass.Events().Subscribe(roomBypass, 0)
	defer unsubBypass()
	if _, err := engBypass.SubmitTurn(t.Context(), roomBypass, runtime.SubmitTurnRequest{
		RequestID: "req-bypass", Text: "Read hello.txt",
	}); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, chBypass, "turn.completed", 5*time.Second)

	// 2. Request mode pauses at awaiting_approval, resumes on approve, rejects cleanly on reject.
	var reqRound atomic.Int32
	engReq, roomReq, _, _ := setupToolRuntime(t, domain.ApprovalRequest, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"chat-1"}]}`)
		case "/v1/chat/completions":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"stream":true`) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"r","object":"chat.completion","model":"chat-1","choices":[{"index":0,"message":{"role":"assistant","content":"[]"},"finish_reason":"stop"}]}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			switch reqRound.Add(1) {
			case 1:
				_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"hello.txt\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
			case 2:
				_, _ = io.WriteString(w, `data: {"id":"c2","object":"chat.completion.chunk","model":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_q","type":"function","function":{"name":"ask_to_user","arguments":"{\"prompt\":\"Confirm tone?\",\"choices\":[\"Formal\",\"Casual\"]}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
			default:
				if !strings.Contains(string(body), "Casual") {
					t.Fatalf("expected user answer Casual in prompt: %s", string(body))
				}
				_, _ = io.WriteString(w, `data: {"id":"c3","object":"chat.completion.chunk","model":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","content":"Done in casual tone!"},"finish_reason":"stop"}]}`+"\n\n")
			}
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}
	}))

	chReq, unsubReq := engReq.Events().Subscribe(roomReq, 0)
	defer unsubReq()
	if _, err := engReq.SubmitTurn(t.Context(), roomReq, runtime.SubmitTurnRequest{
		RequestID: "req-approve-ask", Text: "Help me read hello.txt and reply.",
	}); err != nil {
		t.Fatal(err)
	}

	evApp := waitForEvent(t, chReq, "approval.required", 5*time.Second)
	inv := evApp.Data.(map[string]any)["invocation"].(domain.ToolInvocation)
	if _, err := engReq.ResolveApproval(t.Context(), roomReq, inv.ID, "approve", "looks safe"); err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}

	evQ := waitForEvent(t, chReq, "question.required", 5*time.Second)
	q := evQ.Data.(map[string]any)["question"].(domain.UserQuestion)
	if _, err := engReq.ResolveQuestion(t.Context(), roomReq, q.ID, "Casual"); err != nil {
		t.Fatalf("ResolveQuestion: %v", err)
	}
	evDone := waitForEvent(t, chReq, "turn.completed", 5*time.Second)
	var doneMap map[string]any
	rawDone, _ := json.Marshal(evDone.Data)
	_ = json.Unmarshal(rawDone, &doneMap)
	if doneMap["status"] != "completed" {
		t.Fatalf("expected completed, got %#v", doneMap)
	}

	// 3. Request mode reject sends rejection message cleanly to LLM and completes turn.
	var rejRound atomic.Int32
	engRej, roomRej, _, _ := setupToolRuntime(t, domain.ApprovalRequest, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"chat-1"}]}`)
		case "/v1/chat/completions":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"stream":true`) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"r","object":"chat.completion","model":"chat-1","choices":[{"index":0,"message":{"role":"assistant","content":"[]"},"finish_reason":"stop"}]}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			if rejRound.Add(1) == 1 {
				_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_r","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"hello.txt\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
			} else {
				if !strings.Contains(string(body), "rejected by user") {
					t.Fatalf("expected rejection text in resumed prompt: %s", string(body))
				}
				_, _ = io.WriteString(w, `data: {"id":"c2","object":"chat.completion.chunk","model":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","content":"Understood, skipped reading the file."},"finish_reason":"stop"}]}`+"\n\n")
			}
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}
	}))
	chRej, unsubRej := engRej.Events().Subscribe(roomRej, 0)
	defer unsubRej()
	if _, err := engRej.SubmitTurn(t.Context(), roomRej, runtime.SubmitTurnRequest{
		RequestID: "req-reject", Text: "Read hello.txt if allowed.",
	}); err != nil {
		t.Fatal(err)
	}
	evRejApp := waitForEvent(t, chRej, "approval.required", 5*time.Second)
	invRej := evRejApp.Data.(map[string]any)["invocation"].(domain.ToolInvocation)
	if _, err := engRej.ResolveApproval(t.Context(), roomRej, invRej.ID, "reject", "private file"); err != nil {
		t.Fatalf("ResolveApproval reject: %v", err)
	}
	waitForEvent(t, chRej, "turn.completed", 5*time.Second)
}

func TestIdempotentSubmitWhileAwaitingApprovalOrUserDoesNotDuplicate(t *testing.T) {
	var round atomic.Int32
	eng, roomID, _, _ := setupToolRuntime(t, domain.ApprovalRequest, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"chat-1"}]}`)
		case "/v1/chat/completions":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"stream":true`) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"r","object":"chat.completion","model":"chat-1","choices":[{"index":0,"message":{"role":"assistant","content":"[]"},"finish_reason":"stop"}]}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			switch round.Add(1) {
			case 1:
				_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"hello.txt\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
			case 2:
				_, _ = io.WriteString(w, `data: {"id":"c2","object":"chat.completion.chunk","model":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_q","type":"function","function":{"name":"ask_to_user","arguments":"{\"prompt\":\"Continue?\",\"choices\":[\"Yes\",\"No\"]}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
			default:
				_, _ = io.WriteString(w, `data: {"id":"c3","object":"chat.completion.chunk","model":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","content":"All set."},"finish_reason":"stop"}]}`+"\n\n")
			}
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}
	}))

	ch, unsub := eng.Events().Subscribe(roomID, 0)
	defer unsub()

	req := runtime.SubmitTurnRequest{RequestID: "req-dup-await", Text: "Inspect hello.txt"}
	res1, err := eng.SubmitTurn(t.Context(), roomID, req)
	if err != nil {
		t.Fatal(err)
	}
	evApp := waitForEvent(t, ch, "approval.required", 5*time.Second)
	inv := evApp.Data.(map[string]any)["invocation"].(domain.ToolInvocation)

	// Re-submit identical request_id while awaiting_approval: must return same turn without spawning another approval.
	res2, err := eng.SubmitTurn(t.Context(), roomID, req)
	if err != nil {
		t.Fatalf("duplicate SubmitTurn during awaiting_approval: %v", err)
	}
	if res2.Turn.ID != res1.Turn.ID || res2.Turn.Status != domain.TurnStatusAwaitingApproval {
		t.Fatalf("unexpected res2 = %+v", res2)
	}
	select {
	case ev := <-ch:
		if ev.Type == "approval.required" || ev.Type == "turn.started" {
			t.Fatalf("duplicate awaiting submission emitted spurious event %s", ev.Type)
		}
	case <-time.After(100 * time.Millisecond):
	}

	if _, err := eng.ResolveApproval(t.Context(), roomID, inv.ID, "approve", "ok"); err != nil {
		t.Fatal(err)
	}
	evQ := waitForEvent(t, ch, "question.required", 5*time.Second)
	q := evQ.Data.(map[string]any)["question"].(domain.UserQuestion)

	// Re-submit identical request_id while awaiting_user: must return same turn without duplicating question.
	res3, err := eng.SubmitTurn(t.Context(), roomID, req)
	if err != nil {
		t.Fatalf("duplicate SubmitTurn during awaiting_user: %v", err)
	}
	if res3.Turn.ID != res1.Turn.ID || res3.Turn.Status != domain.TurnStatusAwaitingUser {
		t.Fatalf("unexpected res3 = %+v", res3)
	}
	select {
	case ev := <-ch:
		if ev.Type == "question.required" || ev.Type == "turn.started" {
			t.Fatalf("duplicate awaiting_user submission emitted spurious event %s", ev.Type)
		}
	case <-time.After(100 * time.Millisecond):
	}

	if _, err := eng.ResolveQuestion(t.Context(), roomID, q.ID, "Yes"); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, ch, "turn.completed", 5*time.Second)
}

func TestAppendOnlyPromptPrefixAndCompactionEpoch(t *testing.T) {
	type wireMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type capturedReq struct {
		PromptCacheKey string    `json:"prompt_cache_key"`
		Messages       []wireMsg `json:"messages"`
	}

	reqCh := make(chan capturedReq, 16)
	var streamRound atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"chat-1"}]}`)
		case "/v1/chat/completions":
			body, _ := io.ReadAll(r.Body)
			var probe struct {
				Stream         bool      `json:"stream"`
				PromptCacheKey string    `json:"prompt_cache_key"`
				Messages       []wireMsg `json:"messages"`
			}
			_ = json.Unmarshal(body, &probe)
			if !probe.Stream {
				w.Header().Set("Content-Type", "application/json")
				if len(probe.Messages) > 0 && strings.Contains(probe.Messages[0].Content, "running memory summary") {
					_, _ = io.WriteString(w, `{"id":"sum1","object":"chat.completion","model":"chat-1","choices":[{"index":0,"message":{"role":"assistant","content":"Compacted epoch summary."},"finish_reason":"stop"}]}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"r","object":"chat.completion","model":"chat-1","choices":[{"index":0,"message":{"role":"assistant","content":"[]"},"finish_reason":"stop"}]}`)
				return
			}
			reqCh <- capturedReq{
				PromptCacheKey: probe.PromptCacheKey,
				Messages:       probe.Messages,
			}
			w.Header().Set("Content-Type", "text/event-stream")
			switch streamRound.Add(1) {
			case 1:
				_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","content":"Found amber beacon."},"finish_reason":"stop"}]}`+"\n\n")
			case 2:
				_, _ = io.WriteString(w, `data: {"id":"c2","object":"chat.completion.chunk","model":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_read","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"hello.txt\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
			case 3:
				_, _ = io.WriteString(w, `data: {"id":"c3","object":"chat.completion.chunk","model":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","content":"Checked hello.txt and cobalt harbor."},"finish_reason":"stop"}]}`+"\n\n")
			default:
				_, _ = io.WriteString(w, `data: {"id":"c4","object":"chat.completion.chunk","model":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","content":"Compacted epoch response."},"finish_reason":"stop"}]}`+"\n\n")
			}
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	workDir1 := filepath.Join(dir, "w1")
	workDir2 := filepath.Join(dir, "w2")
	_ = os.MkdirAll(workDir1, 0o755)
	_ = os.MkdirAll(workDir2, 0o755)
	_ = os.WriteFile(filepath.Join(workDir2, "hello.txt"), []byte("hello from workspace"), 0o644)

	rdb, err := sqlite.Open(t.Context(), filepath.Join(dir, "q.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	idx, err := blevehnsw.Open(filepath.Join(dir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	blobs, _ := localfs.Open(filepath.Join(dir, "blobs"))
	defer blobs.Close()

	cfg := config.Default()
	cfg.Tools.ApprovalMode = domain.ApprovalBypass
	cfg.Tools.ToolOverrides = nil
	cfg.Tools.AllowedRoots = []string{workDir1, workDir2}
	cfg.LLM.Providers = []gateway.ProviderConfig{{
		ID: "mock", Type: "openai-compatible", Enabled: true, BaseURL: srv.URL + "/v1",
	}}
	cfg.LLM.Roles.ChatModel = "mock/chat-1"
	cfg.LLM.Roles.MemoryModel = "mock/chat-1"

	gw, err := gateway.NewContext(t.Context(), cfg.GatewayConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()

	persona, _ := rdb.Personas().Create(t.Context(), domain.Persona{Name: "Alex"})
	char, _ := rdb.Characters().Create(t.Context(), domain.Character{
		Name:       "Noa",
		ModelPrefs: &domain.ModelPrefs{ContextWindowOverride: 450},
	})
	room, _ := rdb.Rooms().Create(t.Context(), domain.Room{
		Title: "Workshop", Mode: domain.RoomModeDirect, PersonaID: persona.ID, PersonaSnapshot: persona, WorkDir: workDir1,
	}, []domain.RoomMember{{
		CharacterID: char.ID, CharacterSnapshot: char, Active: true,
	}})

	seedTurn, _, _ := rdb.Turns().CreateOrGetIdempotent(t.Context(), domain.Turn{
		RoomID: room.ID, RequestID: "seed-mem", InputHash: "h", Status: domain.TurnStatusCompleted,
	})
	_ = rdb.Memories().CreateMemory(t.Context(), domain.Memory{
		ID: "mem-amber", RoomID: room.ID, OwnerID: char.ID, SourceTurnID: seedTurn.ID,
		Kind: domain.MemoryKindEpisodic, Content: "amber beacon sits on pier 3",
		ObservedBy: []string{char.ID}, OccurredAt: 1000, Status: domain.MemoryStatusActive, Revision: 1,
	})
	_ = idx.Upsert(t.Context(), storage.SearchDocument{
		ID: "mem-amber", EntityType: "memory", Scope: "room:" + room.ID, OwnerID: char.ID,
		ObservedBy: []string{char.ID}, Text: "amber beacon sits on pier 3", OccurredAt: 1000, Revision: 1,
	})

	reg := task.NewRegistry(task.RegistryDeps{RDB: rdb, Search: idx, Loom: task.NewLoomStore(rdb, blobs, 64<<20)})
	eng := runtime.NewEngine(rdb, idx, staticProviders{snap: runtime.ProviderSnapshot{
		Config:  cfg,
		Gateway: gw,
	}}, nil)
	eng.SetTools(reg, dir)
	defer eng.Close()

	ch, unsub := eng.Events().Subscribe(room.ID, 0)
	defer unsub()

	// Turn 1: establishes baseline prompt & cache key.
	if _, err := eng.SubmitTurn(t.Context(), room.ID, runtime.SubmitTurnRequest{
		RequestID: "req-ao-1",
		Text:      "amber beacon",
	}); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, ch, "turn.completed", 5*time.Second)
	r1 := <-reqCh

	// Mutate actor affect, values, room WorkDir, and add a new memory BEFORE Turn 2.
	room.WorkDir = workDir2
	room, err = rdb.Rooms().Update(t.Context(), room)
	if err != nil {
		t.Fatal(err)
	}
	mems, _ := rdb.Rooms().ListMembers(t.Context(), room.ID)
	mems[0].ActorState.Self.Valence = 75
	mems[0].ActorState.Values = []domain.CharacterValue{{ID: "val-1", Text: "Stay curious."}}
	if err := rdb.Rooms().UpsertMember(t.Context(), mems[0]); err != nil {
		t.Fatal(err)
	}
	_ = rdb.Memories().CreateMemory(t.Context(), domain.Memory{
		ID: "mem-cobalt", RoomID: room.ID, OwnerID: char.ID, SourceTurnID: seedTurn.ID,
		Kind: domain.MemoryKindEpisodic, Content: "cobalt harbor ledger is inside hello.txt",
		ObservedBy: []string{char.ID}, OccurredAt: 1500, Status: domain.MemoryStatusActive, Revision: 1,
	})
	_ = idx.Upsert(t.Context(), storage.SearchDocument{
		ID: "mem-cobalt", EntityType: "memory", Scope: "room:" + room.ID, OwnerID: char.ID,
		ObservedBy: []string{char.ID}, Text: "cobalt harbor ledger is inside hello.txt", OccurredAt: 1500, Revision: 1,
	})

	// Turn 2: runs read_file tool call (produces round 1 & round 2 requests).
	if _, err := eng.SubmitTurn(t.Context(), room.ID, runtime.SubmitTurnRequest{
		RequestID: "req-ao-2",
		Text:      "cobalt harbor ledger",
	}); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, ch, "turn.completed", 5*time.Second)
	r2 := <-reqCh
	r3 := <-reqCh

	// 1. Only one system message at index 0; byte-for-byte identical across Turn 1 & Turn 2 rounds.
	for idxReq, req := range []capturedReq{r1, r2, r3} {
		sysCount := 0
		for _, m := range req.Messages {
			if m.Role == "system" {
				sysCount++
			}
		}
		if sysCount != 1 || req.Messages[0].Role != "system" {
			t.Fatalf("request %d expected single system message at index 0, got %+v", idxReq, req.Messages)
		}
	}
	if r1.PromptCacheKey == "" || r1.PromptCacheKey != r2.PromptCacheKey || r2.PromptCacheKey != r3.PromptCacheKey {
		t.Fatalf("expected identical prompt_cache_key across Turn 1 and Turn 2 rounds, got %q / %q / %q", r1.PromptCacheKey, r2.PromptCacheKey, r3.PromptCacheKey)
	}

	// 2. Turn 1 entire prefix is byte-for-byte preserved in Turn 2 round 1 and Turn 2 round 2.
	if len(r2.Messages) <= len(r1.Messages) || len(r3.Messages) <= len(r2.Messages) {
		t.Fatalf("expected strictly growing append-only messages: r1=%d r2=%d r3=%d", len(r1.Messages), len(r2.Messages), len(r3.Messages))
	}
	for i := range r1.Messages {
		if r2.Messages[i] != r1.Messages[i] {
			t.Fatalf("r2 prefix diverged from r1 at msg[%d]:\nwant=%+v\ngot =%+v", i, r1.Messages[i], r2.Messages[i])
		}
		if r3.Messages[i] != r1.Messages[i] {
			t.Fatalf("r3 prefix diverged from r1 at msg[%d]:\nwant=%+v\ngot =%+v", i, r1.Messages[i], r3.Messages[i])
		}
	}
	for i := range r2.Messages {
		if r3.Messages[i] != r2.Messages[i] {
			t.Fatalf("r3 tool-round prefix diverged from r2 at msg[%d]:\nwant=%+v\ngot =%+v", i, r2.Messages[i], r3.Messages[i])
		}
	}

	// 3. Updated state + new recall appear only in Turn 2's appended turn_context, never mutating Turn 1.
	t2TurnCtx := r2.Messages[len(r2.Messages)-1].Content
	for _, needle := range []string{"<turn_context>", "valence=75", "Stay curious.", workDir2, "cobalt harbor ledger is inside hello.txt"} {
		if !strings.Contains(t2TurnCtx, needle) {
			t.Fatalf("expected Turn 2 turn_context to contain %q, got %q", needle, t2TurnCtx)
		}
	}

	// Turn 3: trigger 85% compaction and verify PromptCache epoch advances cleanly.
	if _, err := eng.SubmitTurn(t.Context(), room.ID, runtime.SubmitTurnRequest{
		RequestID: "req-ao-3",
		Text:      strings.Repeat("compaction filler entry ", 35),
	}); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, ch, "turn.completed", 5*time.Second)
	r4 := <-reqCh
	if r4.PromptCacheKey == "" || r4.PromptCacheKey == r1.PromptCacheKey {
		t.Fatalf("expected prompt_cache_key to advance to new compaction epoch, before=%q after=%q", r1.PromptCacheKey, r4.PromptCacheKey)
	}
	afterMems, _ := rdb.Rooms().ListMembers(t.Context(), room.ID)
	pc := afterMems[0].ActorState.PromptCache
	if afterMems[0].ActorState.CompactedThroughSeq == 0 || pc == nil || pc.EpochSeq != afterMems[0].ActorState.CompactedThroughSeq {
		t.Fatalf("expected PromptCache.EpochSeq synced to CompactedThroughSeq=%d, got %+v", afterMems[0].ActorState.CompactedThroughSeq, pc)
	}
	for _, inj := range pc.Injections {
		if inj.AfterSeq <= afterMems[0].ActorState.CompactedThroughSeq {
			t.Fatalf("expected compacted injections (<= %d) pruned, still found %+v", afterMems[0].ActorState.CompactedThroughSeq, inj)
		}
	}
}
