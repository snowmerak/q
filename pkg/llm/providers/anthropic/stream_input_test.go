package anthropic

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func TestStreamToolInputPreservesEmptyAndInitialArguments(t *testing.T) {
	for _, test := range []struct {
		name, input string
		deltas      []string
		want        string
	}{
		{"no deltas", `{}`, nil, `{}`},
		{"empty delta", `{}`, []string{""}, `{}`},
		{"initial input", `{"key":"demo"}`, nil, `{"key":"demo"}`},
		{"fragmented input", `{}`, []string{`{"key":`, `"demo"}`}, `{"key":"demo"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if _, exists := request["stream_options"]; exists {
					t.Error("OpenAI stream_options leaked into Messages request")
					w.WriteHeader(400)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(event string, body any) {
					data, _ := json.Marshal(body)
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
				}
				emit("message_start", map[string]any{"message": map[string]any{"id": "msg", "model": "test", "usage": map[string]int{"input_tokens": 10}}})
				emit("content_block_start", map[string]any{"index": 1, "content_block": map[string]any{"type": "tool_use", "id": "call", "name": "lookup", "input": json.RawMessage(test.input)}})
				for _, delta := range test.deltas {
					emit("content_block_delta", map[string]any{"index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": delta}})
				}
				emit("content_block_stop", map[string]any{"index": 1})
				emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]int{"output_tokens": 1}})
				emit("message_stop", map[string]any{})
			}))
			defer server.Close()
			p := New(WithBaseURL(server.URL), WithAPIKey("test"))
			stream, err := p.ChatStream(t.Context(), llmprovider.ChatRequest{Model: "test", Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "Use lookup"}}, Extra: map[string]any{"stream_options": map[string]any{"include_usage": true}}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stream.Close() }()
			var args, name, id, finish string
			for {
				chunk, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, choice := range chunk.Choices {
					if choice.FinishReason != "" {
						finish = choice.FinishReason
					}
					if choice.Delta == nil {
						continue
					}
					for _, call := range choice.Delta.ToolCalls {
						if call.Index != 1 {
							t.Fatal("content block index changed")
						}
						args += call.Function.Arguments
						name += call.Function.Name
						if call.ID != "" {
							id = call.ID
						}
					}
				}
			}
			if args != test.want || name != "lookup" || id != "call" || finish != "tool_calls" {
				t.Fatalf("args=%q name=%q id=%q finish=%q", args, name, id, finish)
			}
		})
	}
}

func TestUnfinishedToolBlockDoesNotSynthesizeArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call\",\"name\":\"lookup\",\"input\":{}}}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"max_tokens\"}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	p := New(WithBaseURL(server.URL), WithAPIKey("test"))
	stream, err := p.ChatStream(t.Context(), llmprovider.ChatRequest{Model: "test", Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "Use lookup"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	var arguments, finish string
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
			if choice.Delta != nil {
				for _, call := range choice.Delta.ToolCalls {
					arguments += call.Function.Arguments
				}
			}
		}
	}
	if arguments != "" || finish != "length" {
		t.Fatalf("unfinished block was accepted: args=%q finish=%q", arguments, finish)
	}
}
