package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func TestChatNewUserAfterToolHistoryReusesThread(t *testing.T) {
	for _, toolName := range []string{"task_complete", "lookup_value"} {
		for _, inferred := range []bool{false, true} {
			for _, loaded := range []bool{false, true} {
				name := fmt.Sprintf("%s/inferred=%v/loaded=%v", toolName, inferred, loaded)
				t.Run(name, func(t *testing.T) {
					fake := newFakeTransport()
					provider := New(func(cfg *config) {
						cfg.transportFactoryForTest = func() (transport, error) { return fake, nil }
					})
					defer provider.Close()
					provider.loaded["existing-thread"] = loaded
					history := []llmprovider.Message{
						{Role: llmprovider.RoleUser, Content: "Do the previous task"},
						{Role: llmprovider.RoleAssistant, ToolCalls: []llmprovider.ToolCall{{
							ID: "call-1", Type: llmprovider.ToolTypeFunction,
							Function: llmprovider.FunctionCall{Name: toolName, Arguments: `{}`},
						}}},
						{Role: llmprovider.RoleTool, ToolCallID: "call-1", Name: toolName, Content: `{"outcome":"succeeded"}`},
					}
					assistant := llmprovider.Message{Role: llmprovider.RoleAssistant, Content: "Done"}
					request := llmprovider.ChatRequest{Model: "test-model", ConversationID: "existing-thread", Messages: history}
					request.Tools = []llmprovider.Tool{{Type: llmprovider.ToolTypeFunction, Function: llmprovider.FunctionDefinition{
						Name: toolName, Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
					}}}
					if inferred {
						provider.saveConversationCheckpoint(request, assistant, "existing-thread", "previous-turn")
						request.ConversationID = ""
					}
					request.Messages = append(request.Messages, assistant, llmprovider.Message{
						Role: llmprovider.RoleUser, Content: "Now do the next task",
					})

					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					serverErr := make(chan error, 1)
					go func() {
						serverErr <- serveNewUserAfterToolHistory(ctx, fake, !loaded)
					}()
					response, err := provider.Chat(ctx, request)
					if err != nil {
						t.Fatal(err)
					}
					if response.ConversationID != "existing-thread" {
						t.Fatalf("new user request did not retain the thread: %#v", response)
					}
					if err := <-serverErr; err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func serveNewUserAfterToolHistory(ctx context.Context, fake *fakeTransport, wantResume bool) error {
	wantMethods := []string{"initialize", "initialized"}
	if wantResume {
		wantMethods = append(wantMethods, "thread/resume")
	}
	wantMethods = append(wantMethods, "turn/start")
	var methods []string
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
			methods = append(methods, request.Method)
			switch request.Method {
			case "initialize":
				fake.send(map[string]any{"id": request.ID, "result": map[string]any{}})
			case "initialized":
			case "thread/resume":
				if _, exists := request.Params["dynamicTools"]; exists {
					fake.send(map[string]any{"id": request.ID, "error": map[string]any{
						"code": -32602, "message": "unsupported dynamicTools on thread/resume",
					}})
					return fmt.Errorf("thread/resume does not support dynamicTools: %#v", request.Params)
				}
				if _, exists := request.Params["ephemeral"]; exists {
					return fmt.Errorf("thread/resume does not support ephemeral: %#v", request.Params)
				}
				if request.Params["threadId"] != "existing-thread" {
					return fmt.Errorf("resumed wrong thread: %#v", request.Params)
				}
				if request.Params["excludeTurns"] != true {
					return fmt.Errorf("resume should not hydrate unused history: %#v", request.Params)
				}
				fake.send(map[string]any{"id": request.ID, "result": map[string]any{
					"thread": map[string]any{"id": "existing-thread"},
				}})
			case "turn/start":
				if request.Params["threadId"] != "existing-thread" {
					return fmt.Errorf("started turn on wrong thread: %#v", request.Params)
				}
				wantInput := []any{map[string]any{"type": "text", "text": "Now do the next task"}}
				if !reflect.DeepEqual(request.Params["input"], wantInput) {
					return fmt.Errorf("new user input = %#v", request.Params["input"])
				}
				fake.send(map[string]any{"id": request.ID, "result": map[string]any{
					"turn": map[string]any{"id": "next-turn"},
				}})
				fake.send(map[string]any{"method": "turn/completed", "params": map[string]any{
					"threadId": "existing-thread", "turn": map[string]any{"id": "next-turn", "status": "completed"},
				}})
				if !reflect.DeepEqual(methods, wantMethods) {
					return fmt.Errorf("RPC methods = %v, want %v", methods, wantMethods)
				}
				return nil
			default:
				// Reply so a regression fails immediately instead of timing out.
				fake.send(map[string]any{"id": request.ID, "error": map[string]any{
					"code": -32601, "message": "unexpected method: " + request.Method,
				}})
				return fmt.Errorf("unexpected method after completed tool history: %s", request.Method)
			}
		}
	}
}
