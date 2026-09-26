package client_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/workspace"
)

func TestSavedChatSessionCanUseEitherAPIRoute(t *testing.T) {
	history := []client.Message{
		{Role: client.RoleUser, ContentParts: []client.MessageContentPart{
			{"type": "text", "text": "Find this image."},
			{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AA=="}},
		}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "call_1", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "lookup", Arguments: `{}`}}}},
		{Role: client.RoleTool, ToolCallID: "call_1", Content: "result"},
		{Role: client.RoleAssistant, Content: "Previous answer."},
		{Role: client.RoleUser, Content: "Continue."},
	}
	store := workspace.Store{Root: t.TempDir()}
	if err := store.Save(workspace.Session{Context: history}); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	var responseInput []any
	var chatMessages []any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		switch request.URL.Path {
		case "/v1/responses":
			responseInput, _ = body["input"].([]any)
			_, _ = io.WriteString(writer, `{"id":"resp_1","model":"native/model","status":"completed","output":[{"type":"message","phase":"final_answer","content":[{"type":"output_text","text":"Responses answer."}]}]}`)
		case "/v1/chat/completions":
			chatMessages, _ = body["messages"].([]any)
			_, _ = io.WriteString(writer, `{"id":"chat_1","model":"native/model","choices":[{"index":0,"message":{"role":"assistant","content":"Chat answer."}}]}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	for _, mode := range []string{"responses", "chat_completions"} {
		configured, err := client.New(client.Config{BaseURL: server.URL + "/v1", APIKey: "test", ModelAPIModes: map[string]string{"native/model": mode}})
		if err != nil {
			t.Fatal(err)
		}
		reply, err := configured.Chat(t.Context(), client.ChatRequest{Model: "native/model", Messages: restored.Context})
		_ = configured.Close()
		if err != nil || reply == nil || len(reply.Choices) != 1 || reply.Choices[0].Message.Content == "" {
			t.Fatalf("%s reply = %#v, err = %v", mode, reply, err)
		}
	}
	if len(responseInput) != 5 || responseInput[0].(map[string]any)["content"].([]any)[1].(map[string]any)["type"] != "input_image" || responseInput[1].(map[string]any)["call_id"] != "call_1" || responseInput[2].(map[string]any)["type"] != "function_call_output" || responseInput[3].(map[string]any)["phase"] != "final_answer" {
		t.Fatalf("Responses input from saved Chat session = %#v", responseInput)
	}
	if len(chatMessages) != len(history) || chatMessages[0].(map[string]any)["content"].([]any)[1].(map[string]any)["type"] != "image_url" || chatMessages[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["id"] != "call_1" {
		t.Fatalf("Chat input from saved session = %#v", chatMessages)
	}
}
