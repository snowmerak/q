package chatgpt

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func TestChatStreamKeepsToolOnlyOutput(t *testing.T) {
	stream := &chatStream{model: "test", phases: make(map[int]string), inner: &responseStream{inner: &events{queue: []*llmprovider.ResponseEvent{
		{Data: json.RawMessage(`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs-1","type":"reasoning","encrypted_content":"opaque"}}`)},
		{Data: json.RawMessage(`{"type":"response.output_item.done","output_index":1,"item":{"id":"fc-1","type":"function_call","call_id":"call-1","name":"lookup","namespace":"functions","arguments":"{}"}}`)},
		{Data: json.RawMessage(`{"type":"response.completed","response":{"id":"resp-test","model":"canonical-test","status":"completed","output":[]}}`)},
	}}}}
	chunk, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	message := chunk.Choices[0].Delta
	if chunk.Choices[0].FinishReason != "tool_calls" || message.Content != "" || len(message.ToolCalls) != 1 || message.ToolCalls[0].ID != "call-1" || len(message.ResponseOutput) != 2 || message.ResponseModel != "test" {
		t.Fatalf("tool-only response lost: %+v", chunk)
	}
	body, err := chatBody(llmprovider.ChatRequest{Model: "test", Messages: []llmprovider.Message{*message, {Role: llmprovider.RoleTool, ToolCallID: "call-1", Content: "42"}}})
	if err != nil || !strings.Contains(string(body), `"namespace":"functions"`) || !strings.Contains(string(body), `"encrypted_content":"opaque"`) || !strings.Contains(string(body), `"type":"function_call_output"`) {
		t.Fatalf("lost tool/reasoning replay: %s, %v", body, err)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("completed stream returned %v", err)
	}
}

func TestChatRefusalIsVisibleAndReplayed(t *testing.T) {
	const refusal = "I cannot help with that."
	item := json.RawMessage(`{"id":"msg-refusal","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"refusal","refusal":"I cannot help with that."}]}`)
	body := json.RawMessage(`{"id":"resp-refusal","model":"test","status":"completed","output":[` + string(item) + `]}`)
	response, err := responseChat(body)
	if err != nil {
		t.Fatal(err)
	}
	message := response.Choices[0].Message
	if message.Content != refusal || len(message.ResponseOutput) != 1 {
		t.Fatalf("lost refusal: %+v", message)
	}
	replay, err := chatBody(llmprovider.ChatRequest{Model: "test", Messages: []llmprovider.Message{message}})
	if err != nil || !strings.Contains(string(replay), `"refusal":"`+refusal+`"`) {
		t.Fatalf("refusal replay: %s, %v", replay, err)
	}
	for _, deltas := range []bool{false, true} {
		t.Run(map[bool]string{false: "terminal-only", true: "deltas"}[deltas], func(t *testing.T) {
			queue := []*llmprovider.ResponseEvent{}
			if deltas {
				queue = append(queue, &llmprovider.ResponseEvent{Data: json.RawMessage(`{"type":"response.refusal.delta","output_index":0,"delta":"I cannot help "}`)}, &llmprovider.ResponseEvent{Data: json.RawMessage(`{"type":"response.refusal.delta","output_index":0,"delta":"with that."}`)})
			}
			queue = append(queue, &llmprovider.ResponseEvent{Data: json.RawMessage(`{"type":"response.output_item.done","output_index":0,"item":` + string(item) + `}`)}, &llmprovider.ResponseEvent{Data: json.RawMessage(`{"type":"response.completed","response":{"id":"resp-refusal","model":"test","status":"completed","output":[]}}`)})
			stream := &chatStream{inner: &responseStream{inner: &events{queue: queue}}, model: "test", phases: make(map[int]string)}
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
					if choice.FinishReason != "" {
						native = choice.Delta.ResponseOutput
					}
				}
			}
			if text.String() != refusal || len(native) != 1 {
				t.Fatalf("lost or duplicated refusal: text=%q native=%s", text.String(), native)
			}
		})
	}
}

func TestResponseStreamPreservesFailureDetails(t *testing.T) {
	for _, test := range []struct {
		name, data string
		want       []string
	}{
		{"plan-limit", `{"type":"response.failed","response":{"status":"failed","error":{"code":"subscription_sharing_usage_limit_exceeded","message":"Retry after the plan resets"}}}`, []string{"subscription_sharing_usage_limit_exceeded", "Retry after the plan resets"}},
		{"top-level", `{"type":"error","code":"invalid_request","message":"Missing field input"}`, []string{"invalid_request", "Missing field input"}},
		{"nested-error", `{"type":"error","error":{"code":"unavailable","message":"Try again later"}}`, []string{"unavailable", "Try again later"}},
		{"incomplete", `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`, []string{"response.incomplete", "content_filter"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := &responseStream{inner: &events{queue: []*llmprovider.ResponseEvent{{Data: json.RawMessage(test.data)}}}}
			_, err := stream.Recv()
			if err == nil {
				t.Fatal("failed response accepted")
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("missing %q in %v", want, err)
				}
			}
			if _, err := stream.Recv(); err != io.EOF {
				t.Fatalf("failure must end the stream: %v", err)
			}
		})
	}
}
