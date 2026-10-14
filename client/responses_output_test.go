package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesStreamMergesCompletedOutput(t *testing.T) {
	items := []json.RawMessage{
		json.RawMessage(`{"id":"rs-1","type":"reasoning","encrypted_content":"opaque"}`),
		json.RawMessage(`{"id":"msg-1","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hello"}]}`),
		json.RawMessage(`{"id":"fc-1","type":"function_call","call_id":"call-1","name":"lookup","arguments":"{}"}`),
	}
	for _, shape := range []string{"empty", "absent", "null", "partial", "complete"} {
		t.Run(shape, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":1,\"delta\":\"Hello\"}\n\n")
				for index, item := range items {
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":%d,\"item\":%s}\n\n", index, item)
				}
				terminal := map[string]any{"id": "resp-test", "model": "test", "status": "completed", "usage": map[string]any{"input_tokens": 10, "output_tokens": 3, "total_tokens": 13, "input_tokens_details": map[string]int{"cached_tokens": 5}}}
				switch shape {
				case "empty":
					terminal["output"] = []any{}
				case "null":
					terminal["output"] = nil
				case "partial":
					terminal["output"] = items[1:2]
				case "complete":
					terminal["output"] = items
				}
				body, err := json.Marshal(terminal)
				if err != nil {
					t.Errorf("marshal completion: %v", err)
					return
				}
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", body)
			}))
			t.Cleanup(server.Close)
			configured, err := New(Config{BaseURL: server.URL + "/v1", DisableAPIKey: true, ModelAPIModes: map[string]string{"test": "responses"}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = configured.Close() })
			stream, err := configured.ChatStream(t.Context(), ChatRequest{Model: "test", Messages: []Message{{Role: RoleUser, Content: "hello"}}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = stream.Close() })
			var text strings.Builder
			var completed *ChatChunk
			for {
				chunk, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, choice := range chunk.Choices {
					if choice.Delta != nil {
						text.WriteString(choice.Delta.Content)
						if choice.FinishReason != "" {
							completed = chunk
						}
					}
				}
			}
			if text.String() != "Hello" || completed == nil || completed.Usage == nil || completed.Usage.TotalTokens != 13 || completed.Usage.PromptDetails.CachedTokens != 5 {
				t.Fatalf("lost text or usage: text=%q completed=%+v", text.String(), completed)
			}
			message := completed.Choices[0].Delta
			if len(message.ResponseOutput) != 3 || len(message.ToolCalls) != 1 || completed.Choices[0].FinishReason != "tool_calls" {
				t.Fatalf("lost or duplicated output: %+v", message)
			}
			for index, item := range message.ResponseOutput {
				var actual, expected map[string]any
				_ = json.Unmarshal(item, &actual)
				_ = json.Unmarshal(items[index], &expected)
				if actual["id"] != expected["id"] {
					t.Fatalf("output order changed: %s", message.ResponseOutput)
				}
			}
			replayed, err := responseInput([]Message{*message, {Role: RoleTool, ToolCallID: "call-1", Content: "42"}}, "test")
			if err != nil {
				t.Fatal(err)
			}
			if len(replayed) != 4 || replayed[0].(map[string]any)["encrypted_content"] != "opaque" || replayed[3].(map[string]any)["call_id"] != "call-1" {
				t.Fatalf("lost replay context: %#v", replayed)
			}
		})
	}
}

func TestResponsesRefusalRemainsACompletedAnswer(t *testing.T) {
	const refusal = "I cannot help with that."
	item := json.RawMessage(`{"id":"msg-refusal","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"refusal","refusal":"I cannot help with that."}]}`)
	document := json.RawMessage(`{"id":"resp-refusal","model":"test","status":"completed","output":[` + string(item) + `]}`)
	response, err := parseResponseDocument(document, "cache_test", "test")
	if err != nil {
		t.Fatal(err)
	}
	if response.Choices[0].Message.Content != refusal || response.Choices[0].FinishReason != "stop" {
		t.Fatalf("refusal lost: %+v", response)
	}
	for _, deltas := range []bool{false, true} {
		t.Run(map[bool]string{false: "terminal-only", true: "deltas"}[deltas], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if deltas {
					_, _ = io.WriteString(w, "data: {\"type\":\"response.refusal.delta\",\"output_index\":0,\"delta\":\"I cannot help \"}\n\ndata: {\"type\":\"response.refusal.delta\",\"output_index\":0,\"delta\":\"with that.\"}\n\n")
				}
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", item, document)
			}))
			t.Cleanup(server.Close)
			configured, err := New(Config{BaseURL: server.URL + "/v1", DisableAPIKey: true, ModelAPIModes: map[string]string{"test": "responses"}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = configured.Close() })
			stream, err := configured.ChatStream(t.Context(), ChatRequest{Model: "test", Messages: []Message{{Role: RoleUser, Content: "hello"}}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = stream.Close() })
			var text strings.Builder
			var native []json.RawMessage
			for {
				chunk, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, choice := range chunk.Choices {
					text.WriteString(choice.Delta.Content)
					if choice.FinishReason == "stop" {
						native = choice.Delta.ResponseOutput
					}
				}
			}
			if text.String() != refusal || len(native) != 1 || !strings.Contains(string(native[0]), `"type":"refusal"`) {
				t.Fatalf("refusal lost or duplicated: text=%q native=%s", text.String(), native)
			}
		})
	}
}
