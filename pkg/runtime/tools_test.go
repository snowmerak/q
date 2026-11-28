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
