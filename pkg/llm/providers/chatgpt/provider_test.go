package chatgpt

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func TestNormalizePlanContract(t *testing.T) {
	body, err := normalize(json.RawMessage(`{"model":"test","input":[{"role":"system","content":"instructions"}],"store":true,"stream":false,"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"lookup"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	_ = json.Unmarshal(body, &value)
	if value["store"] != false || value["stream"] != true || value["input"].([]any)[0].(map[string]any)["role"] != "developer" || value["tools"].([]any)[0].(map[string]any)["name"] != "functions" || value["tool_choice"].(map[string]any)["namespace"] != "functions" {
		t.Fatalf("invalid adaptation: %s", body)
	}
	if again, err := normalize(body); err != nil || string(again) != string(body) {
		t.Fatalf("native namespace adaptation is not stable: %s %v", again, err)
	}
	for _, field := range []string{"max_output_tokens", "temperature", "previous_response_id", "conversation", "top_p"} {
		t.Run(field, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"input": "hi", field: 1})
			if _, err := normalize(raw); err == nil {
				t.Fatal("unsupported option silently accepted")
			}
		})
	}
	for _, kind := range []string{"code_interpreter", "image_generation", "mcp", "file_search", "tool_search"} {
		raw, _ := json.Marshal(map[string]any{"input": "hi", "tools": []any{map[string]any{"type": kind}}})
		if _, err := normalize(raw); err == nil {
			t.Errorf("hosted tool %s accepted", kind)
		}
	}
}
func TestNonStreamingAdapterReplaysNativeOutput(t *testing.T) {
	var requests []map[string]any
	terminal := `{"id":"resp-test","model":"test-canonical","status":"completed","output":[{"id":"reasoning-1","type":"reasoning","encrypted_content":"opaque-reasoning"},{"id":"message-1","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"OK"}]},{"type":"function_call","call_id":"call-1","name":"lookup","namespace":"functions","arguments":"{}"}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != resource+"/responses" || r.Header.Get("Authorization") != "Bearer renewed-access" {
			t.Errorf("incorrect resource or credential %s", r.URL)
		}
		if r.Header.Get("originator") != "llm-provider" || r.Header.Get("User-Agent") != "llm-provider/llm-provider" {
			t.Error("request headers changed app identity")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests = append(requests, body)
		if body["stream"] != true || body["store"] != false {
			t.Error("plan transport must stream without storing")
		}
		sse := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + terminal + "}\n\n"
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse)), Request: r}, nil
	})}
	p := newTestProvider(t, t.TempDir(), client)
	seedAccount(t, p)
	_ = p.withState(context.Background(), func(s *state) error {
		a := s.Accounts[s.Active]
		a.Access = "renewed-access"
		a.Expires = 1 << 62
		s.Accounts[s.Active] = a
		return p.save(s)
	})
	request := llmprovider.ChatRequest{Model: "test", Headers: http.Header{"originator": {"other-app"}, "user-agent": {"other-host"}}, Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "hello"}}}
	response, err := p.Chat(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	message := response.Choices[0].Message
	if message.Content != "OK" || len(message.ResponseOutput) != 3 || len(message.ToolCalls) != 1 || message.ResponseModel != "test" || response.Usage.TotalTokens != 5 {
		t.Fatalf("lost terminal output: %+v", response)
	}
	request.Messages = append(request.Messages, message, llmprovider.Message{Role: llmprovider.RoleTool, ToolCallID: "call-1", Content: "42"}, llmprovider.Message{Role: llmprovider.RoleUser, Content: "next"})
	if _, err = p.Chat(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(requests[1]["input"])
	if !strings.Contains(string(input), "opaque-reasoning") || !strings.Contains(string(input), "reasoning-1") || !strings.Contains(string(input), "function_call_output") || !strings.Contains(string(input), `"namespace":"functions"`) {
		t.Fatal("stateless history lost native reasoning")
	}
	stream, err := p.ChatStream(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	var text strings.Builder
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
			}
		}
	}
	if text.String() != "OK" {
		t.Fatalf("duplicate/lost streamed text: %q", text.String())
	}
}

type events struct{ queue []*llmprovider.ResponseEvent }

func (s *events) Close() error { return nil }
func (s *events) Recv() (*llmprovider.ResponseEvent, error) {
	if len(s.queue) == 0 {
		return nil, io.EOF
	}
	e := s.queue[0]
	s.queue = s.queue[1:]
	return e, nil
}
func TestTruncatedOrFailedStreamNeverSucceeds(t *testing.T) {
	for _, data := range []string{"", `{"type":"response.failed","response":{"status":"failed","error":{"code":"denied"}}}`, `{"type":"response.incomplete","response":{"status":"incomplete"}}`, `{"type":"response.completed","response":{"status":"failed"}}`} {
		inner := &events{}
		if data != "" {
			inner.queue = []*llmprovider.ResponseEvent{{Data: json.RawMessage(data)}}
		}
		stream := &responseStream{inner: inner}
		if _, err := stream.Recv(); err == nil || err == io.EOF {
			t.Fatalf("failure accepted as complete: %s", data)
		}
	}
}
