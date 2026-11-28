package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func TestChatWithToolsKeepsLoadedThread(t *testing.T) {
	for _, ephemeral := range []bool{false, true} {
		for _, inferred := range []bool{false, true} {
			t.Run(fmt.Sprintf("ephemeral=%v/inferred=%v", ephemeral, inferred), func(t *testing.T) {
				fake := newFakeTransport()
				provider := New(WithEphemeral(ephemeral), func(cfg *config) {
					cfg.transportFactoryForTest = func() (transport, error) { return fake, nil }
				})
				defer provider.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				serverErr := make(chan error, 1)
				go func() { serverErr <- serveLoadedThreadWithTools(ctx, fake, ephemeral) }()
				request := llmprovider.ChatRequest{
					Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "first"}},
					Tools: []llmprovider.Tool{{Type: llmprovider.ToolTypeFunction, Function: llmprovider.FunctionDefinition{
						Name: "lookup", Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
					}}},
				}
				for _, prompt := range []string{"first", "second", "third"} {
					request.Messages[len(request.Messages)-1].Content = prompt
					response, err := provider.Chat(ctx, request)
					if err != nil {
						t.Fatal(err)
					}
					if response.ConversationID != "same-thread" {
						t.Fatalf("conversation changed: %q", response.ConversationID)
					}
					if !inferred {
						request.ConversationID = response.ConversationID
					}
					request.Messages = append(request.Messages, response.Choices[0].Message,
						llmprovider.Message{Role: llmprovider.RoleUser})
				}
				if err := <-serverErr; err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func serveLoadedThreadWithTools(ctx context.Context, fake *fakeTransport, ephemeral bool) error {
	starts, turns := 0, 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case data := <-fake.writes:
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
				fake.send(map[string]any{"id": request.ID, "result": map[string]any{}})
			case "initialized":
			case "thread/start":
				starts++
				tools, ok := request.Params["dynamicTools"].([]any)
				if starts != 1 || request.Params["ephemeral"] != ephemeral || !ok || len(tools) != 1 {
					return fmt.Errorf("unexpected thread/start: %#v", request.Params)
				}
				fake.send(map[string]any{"id": request.ID, "result": map[string]any{
					"thread": map[string]any{"id": "same-thread", "ephemeral": ephemeral},
				}})
			case "turn/start":
				if starts != 1 || request.Params["threadId"] != "same-thread" {
					return fmt.Errorf("unexpected turn/start: %#v", request.Params)
				}
				turns++
				turnID := fmt.Sprintf("turn-%d", turns)
				fake.send(map[string]any{"id": request.ID, "result": map[string]any{"turn": map[string]any{"id": turnID}}})
				fake.send(map[string]any{"method": "item/completed", "params": map[string]any{
					"threadId": "same-thread", "turnId": turnID,
					"item": map[string]any{"id": "answer", "type": "agentMessage", "text": "done"},
				}})
				fake.send(map[string]any{"method": "turn/completed", "params": map[string]any{
					"threadId": "same-thread", "turn": map[string]any{"id": turnID, "status": "completed"},
				}})
				if turns == 3 {
					return nil
				}
			default:
				fake.send(map[string]any{"id": request.ID, "error": map[string]any{
					"code": -32600, "message": "unexpected method: " + request.Method,
				}})
				return fmt.Errorf("unexpected method on a loaded thread: %s", request.Method)
			}
		}
	}
}

func TestThreadClosedClearsLoadedState(t *testing.T) {
	provider := New()
	defer provider.Close()
	provider.loaded["closed-thread"] = true
	provider.loaded["other-thread"] = true
	provider.handleNotification("thread/closed", json.RawMessage(`{"threadId":"closed-thread"}`))
	if provider.loaded["closed-thread"] || !provider.loaded["other-thread"] {
		t.Fatalf("loaded threads after close: %#v", provider.loaded)
	}
	state := newTurnState(t.Context(), provider, "other-thread", "test-model", nil, true)
	provider.active["other-thread"] = state
	provider.handleNotification("thread/closed", json.RawMessage(`{"threadId":"other-thread"}`))
	if provider.loaded["other-thread"] || provider.active["other-thread"] != nil {
		t.Fatal("closed active thread was retained")
	}
	if _, err := (&codexStream{state: state, ctx: t.Context()}).Recv(); err == nil {
		t.Fatal("closing an active thread did not fail its stream")
	}
}

func TestListModelsUsesContextFallbackUntilServerReportsWindow(t *testing.T) {
	fake := newFakeTransport()
	provider := New(func(cfg *config) {
		cfg.transportFactoryForTest = func() (transport, error) { return fake, nil }
	})
	defer provider.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case data := <-fake.writes:
				var request struct {
					ID     int64  `json:"id"`
					Method string `json:"method"`
				}
				_ = json.Unmarshal(data, &request)
				switch request.Method {
				case "initialize":
					fake.send(map[string]any{"id": request.ID, "result": map[string]any{}})
				case "model/list":
					fake.send(map[string]any{"id": request.ID, "result": map[string]any{
						"data": []map[string]any{{"model": "gpt-6.1-sol"}, {"model": "gpt-6-luna"}},
					}})
				}
			}
		}
	}()
	for _, want := range [][]int64{{256000, 128000}, {258400, 129200}} {
		models, err := provider.ListModels(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(models) != 2 || models[0].ContextLength != want[0] || models[1].ContextLength != want[1] {
			t.Fatalf("context windows: %#v, want %v", models, want)
		}
		// Exercise the actual notification contract, including a null window.
		for index, model := range models {
			state := newTurnState(ctx, provider, model.ID, model.ID, nil, true)
			provider.active[model.ID] = state
			window := []int64{258400, 129200}[index]
			params := json.RawMessage(fmt.Sprintf(`{"threadId":%q,"tokenUsage":{"modelContextWindow":%d,"last":{}}}`, model.ID, window))
			provider.handleNotification("thread/tokenUsage/updated", params)
			provider.handleNotification("thread/tokenUsage/updated", json.RawMessage(fmt.Sprintf(
				`{"threadId":%q,"tokenUsage":{"modelContextWindow":null,"last":{}}}`, model.ID)))
		}
	}
}
