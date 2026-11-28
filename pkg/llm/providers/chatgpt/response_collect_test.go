package chatgpt

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func TestResponseRetainsStreamedOutput(t *testing.T) {
	items := []json.RawMessage{
		json.RawMessage(`{"id":"rs-1","type":"reasoning","encrypted_content":"opaque"}`),
		json.RawMessage(`{"id":"msg-1","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hello"}]}`),
		json.RawMessage(`{"id":"fc-1","type":"function_call","call_id":"call-1","name":"lookup","namespace":"functions","arguments":"{}"}`),
	}
	for _, shape := range []string{"empty", "absent", "null", "partial", "complete"} {
		for _, mode := range []string{"responses", "chat", "chat-stream"} {
			t.Run(shape+"/"+mode, func(t *testing.T) {
				transport := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
					var request map[string]any
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						return nil, err
					}
					if _, present := request["stream_options"]; present {
						t.Error("Chat-only stream options leaked into the Responses request")
					}
					var events strings.Builder
					_, _ = fmt.Fprint(&events, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":1,\"delta\":\"Hello\"}\n\n")
					for index, item := range items {
						_, _ = fmt.Fprintf(&events, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":%d,\"item\":%s}\n\n", index, item)
					}
					terminal := map[string]any{"id": "resp-test", "model": "test", "status": "completed", "usage": map[string]int{"input_tokens": 2, "output_tokens": 3, "total_tokens": 5}}
					switch shape {
					case "empty":
						terminal["output"] = []any{}
					case "null":
						terminal["output"] = nil
					case "partial":
						terminal["output"] = items[:1]
					case "complete":
						terminal["output"] = items
					}
					body, err := json.Marshal(terminal)
					if err != nil {
						return nil, err
					}
					_, _ = fmt.Fprintf(&events, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", body)
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"request-test"}}, Body: io.NopCloser(strings.NewReader(events.String())), Request: r}, nil
				})}
				p := newTestProvider(t, t.TempDir(), transport)
				seedAccount(t, p)
				if err := p.withState(t.Context(), func(s *state) error {
					a := s.Accounts[s.Active]
					a.Access, a.Expires = "test-access", 1<<62
					s.Accounts[s.Active] = a
					return p.save(s)
				}); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "responses":
					response, err := p.CreateResponse(t.Context(), json.RawMessage(`{"model":"test","input":"hello"}`), nil)
					if err != nil {
						t.Fatal(err)
					}
					var body struct {
						Output []json.RawMessage
						Usage  struct {
							TotalTokens int `json:"total_tokens"`
						}
					}
					if err := json.Unmarshal(response.Body, &body); err != nil {
						t.Fatal(err)
					}
					if len(body.Output) != 3 || body.Usage.TotalTokens != 5 {
						t.Fatalf("lost completed output or usage: %s", response.Body)
					}
					for index, item := range body.Output {
						var actual, expected map[string]any
						_ = json.Unmarshal(item, &actual)
						_ = json.Unmarshal(items[index], &expected)
						if actual["id"] != expected["id"] {
							t.Fatalf("output order changed: %s", response.Body)
						}
					}
					if !strings.Contains(string(body.Output[0]), `"encrypted_content":"opaque"`) {
						t.Fatal("opaque reasoning lost")
					}
				case "chat":
					response, err := p.Chat(t.Context(), llmprovider.ChatRequest{Model: "test", Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "hello"}}, Extra: map[string]any{"stream_options": map[string]any{"include_usage": true}}})
					if err != nil {
						t.Fatal(err)
					}
					message := response.Choices[0].Message
					if message.Content != "Hello" || len(message.ToolCalls) != 1 || len(message.ResponseOutput) != 3 || response.Usage.TotalTokens != 5 {
						t.Fatalf("lost chat output: %+v", response)
					}
				case "chat-stream":
					stream, err := p.ChatStream(t.Context(), llmprovider.ChatRequest{Model: "test", Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "hello"}}, Extra: map[string]any{"stream_options": map[string]any{"include_usage": true}}})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = stream.Close() })
					var text strings.Builder
					var terminal *llmprovider.ChatChunk
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
									terminal = chunk
								}
							}
						}
					}
					if text.String() != "Hello" || terminal == nil || terminal.Usage == nil || terminal.Usage.TotalTokens != 5 {
						t.Fatalf("lost or duplicated streamed text/usage: text=%q terminal=%+v", text.String(), terminal)
					}
					message := terminal.Choices[0].Delta
					if len(message.ToolCalls) != 1 || len(message.ResponseOutput) != 3 || message.ResponseModel != "test" || !strings.Contains(string(message.ResponseOutput[0]), "opaque") {
						t.Fatalf("lost native output or tool call: %+v", message)
					}
				}
			})
		}
	}
}
