package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func TestMemoryConversationCacheEvictsAndExpires(t *testing.T) {
	cache := NewMemoryConversationCache(1)
	defer cache.Close()
	ctx := context.Background()
	if err := cache.Set(ctx, "first", []byte("one"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := cache.Set(ctx, "second", []byte("two"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := cache.Get(ctx, "first"); err != nil || ok {
		t.Fatalf("evicted entry: ok=%v err=%v", ok, err)
	}
	if value, ok, err := cache.Get(ctx, "second"); err != nil || !ok || string(value) != "two" {
		t.Fatalf("retained entry: value=%q ok=%v err=%v", value, ok, err)
	}
	if err := cache.Set(ctx, "short", []byte("lived"), time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if _, ok, err := cache.Get(ctx, "short"); err != nil || ok {
		t.Fatalf("expired entry: ok=%v err=%v", ok, err)
	}
}

func TestRedisConversationCacheRequiresAddress(t *testing.T) {
	if _, err := NewRedisConversationCache(RedisConversationCacheOptions{}); err == nil {
		t.Fatal("expected missing Redis address error")
	}
}

func TestConversationLineageReusesAndForksThreads(t *testing.T) {
	fake := newFakeTransport()
	provider := New(func(config *config) {
		config.transportFactoryForTest = func() (transport, error) { return fake, nil }
	})
	defer provider.Close()
	serverErr := make(chan error, 1)
	go func() { serverErr <- serveConversationLineage(fake) }()
	client := llmprovider.New(provider)
	ctx := context.Background()

	firstRequest := llmprovider.ChatRequest{Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "first"}}}
	first, err := client.Chat(ctx, firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	if first.ConversationID != "thread_root" || first.Choices[0].Message.Content != "answer-one" {
		t.Fatalf("first response = %#v", first)
	}

	secondRequest := llmprovider.ChatRequest{Messages: []llmprovider.Message{
		{Role: llmprovider.RoleUser, Content: "first"},
		first.Choices[0].Message,
		{Role: llmprovider.RoleUser, Content: "second"},
	}}
	second, err := client.Chat(ctx, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if second.ConversationID != "thread_root" || second.Choices[0].Message.Content != "answer-two" {
		t.Fatalf("second response = %#v", second)
	}

	branchRequest := llmprovider.ChatRequest{Messages: []llmprovider.Message{
		{Role: llmprovider.RoleUser, Content: "first"},
		first.Choices[0].Message,
		{Role: llmprovider.RoleUser, Content: "alternate"},
	}}
	branch, err := client.Chat(ctx, branchRequest)
	if err != nil {
		t.Fatal(err)
	}
	if branch.ConversationID != "thread_branch" || branch.Choices[0].Message.Content != "answer-branch" {
		t.Fatalf("branch response = %#v", branch)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestConversationSessionReusesThreadWithoutHistory(t *testing.T) {
	fake := newFakeTransport()
	provider := New(func(config *config) {
		config.transportFactoryForTest = func() (transport, error) { return fake, nil }
	})
	defer provider.Close()
	serverErr := make(chan error, 1)
	go func() { serverErr <- serveSessionConversation(fake) }()
	client := llmprovider.New(provider)
	ctx := context.Background()

	for _, prompt := range []string{"first", "second"} {
		response, err := client.Chat(ctx, llmprovider.ChatRequest{
			Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: prompt}},
			Extra:    map[string]any{"session_id": "session-a"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if response.ConversationID != "thread_session" {
			t.Fatalf("conversation id = %q", response.ConversationID)
		}
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func serveSessionConversation(transport *fakeTransport) error {
	turn := 0
	starts := 0
	for {
		select {
		case data := <-transport.writes:
			var request struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := json.Unmarshal(data, &request); err != nil {
				return err
			}
			switch request.Method {
			case "initialize":
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{}})
			case "initialized":
			case "thread/start":
				starts++
				if starts > 1 {
					return errors.New("session cache started more than one thread")
				}
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{
					"thread": map[string]any{"id": "thread_session"},
				}})
			case "turn/start":
				if request.Params["threadId"] != "thread_session" {
					return fmt.Errorf("session thread = %#v", request.Params["threadId"])
				}
				turn++
				turnID := fmt.Sprintf("session_turn_%d", turn)
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{
					"turn": map[string]any{"id": turnID, "status": "inProgress"},
				}})
				transport.send(map[string]any{"method": "item/started", "params": map[string]any{
					"threadId": "thread_session", "turnId": turnID,
					"item": map[string]any{"id": "answer", "type": "agentMessage", "phase": "final_answer"},
				}})
				transport.send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{
					"threadId": "thread_session", "turnId": turnID, "itemId": "answer", "delta": "ok",
				}})
				transport.send(map[string]any{"method": "turn/completed", "params": map[string]any{
					"threadId": "thread_session", "turn": map[string]any{"id": turnID, "status": "completed"},
				}})
				if turn == 2 {
					return nil
				}
			default:
				return errors.New("unexpected method: " + request.Method)
			}
		case <-time.After(5 * time.Second):
			return errors.New("timed out waiting for session request")
		}
	}
}

func serveConversationLineage(transport *fakeTransport) error {
	turn := 0
	for {
		select {
		case data := <-transport.writes:
			var request struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := json.Unmarshal(data, &request); err != nil {
				return err
			}
			switch request.Method {
			case "initialize":
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{}})
			case "initialized":
			case "thread/start":
				if turn != 0 {
					return errors.New("lineage continuation unexpectedly started a new root thread")
				}
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{
					"thread": map[string]any{"id": "thread_root"},
				}})
			case "thread/fork":
				if request.Params["threadId"] != "thread_root" || request.Params["lastTurnId"] != "turn_1" {
					return fmt.Errorf("fork params = %#v", request.Params)
				}
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{
					"thread": map[string]any{"id": "thread_branch"},
				}})
			case "turn/start":
				turn++
				threadID, _ := request.Params["threadId"].(string)
				expectedThread := "thread_root"
				answer := "answer-one"
				if turn == 2 {
					answer = "answer-two"
				} else if turn == 3 {
					expectedThread = "thread_branch"
					answer = "answer-branch"
				}
				if threadID != expectedThread {
					return fmt.Errorf("turn %d thread = %q, want %q", turn, threadID, expectedThread)
				}
				turnID := fmt.Sprintf("turn_%d", turn)
				transport.send(map[string]any{"id": request.ID, "result": map[string]any{
					"turn": map[string]any{"id": turnID, "status": "inProgress"},
				}})
				transport.send(map[string]any{"method": "item/started", "params": map[string]any{
					"threadId": threadID, "turnId": turnID,
					"item": map[string]any{"id": "answer", "type": "agentMessage", "phase": "final_answer"},
				}})
				transport.send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{
					"threadId": threadID, "turnId": turnID, "itemId": "answer", "delta": answer,
				}})
				transport.send(map[string]any{"method": "turn/completed", "params": map[string]any{
					"threadId": threadID, "turn": map[string]any{"id": turnID, "status": "completed"},
				}})
				if turn == 3 {
					return nil
				}
			default:
				return errors.New("unexpected method: " + request.Method)
			}
		case <-time.After(5 * time.Second):
			return errors.New("timed out waiting for lineage request")
		}
	}
}
