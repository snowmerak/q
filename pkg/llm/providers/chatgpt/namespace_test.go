package chatgpt

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func TestGeneratedFunctionsNamespaceHasDescription(t *testing.T) {
	body, err := normalize(json.RawMessage(`{"model":"test","input":"hello","tools":[{"type":"namespace","name":"native","description":"Native tools.","tools":[{"type":"function","name":"existing","parameters":{"type":"object"}}]},{"type":"function","name":"lookup","description":"Look up a value.","parameters":{"type":"object"}},{"type":"custom","name":"text_tool","description":"Accept text."}],"tool_choice":{"type":"function","name":"lookup"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Tools []struct {
			Type, Name, Description string
			Tools                   []map[string]any
		}
	}
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Tools) != 2 {
		t.Fatalf("tools = %s", body)
	}
	native, generated := value.Tools[0], value.Tools[1]
	if native.Name != "native" || native.Description != "Native tools." || len(native.Tools) != 1 {
		t.Fatalf("native namespace changed: %s", body)
	}
	if generated.Type != "namespace" || generated.Name != "functions" || strings.TrimSpace(generated.Description) == "" {
		t.Fatalf("generated namespace is missing its required description: %s", body)
	}
	if len(generated.Tools) != 2 || generated.Tools[0]["description"] != "Look up a value." || generated.Tools[1]["type"] != "custom" {
		t.Fatalf("child tools changed: %s", body)
	}
	if again, err := normalize(body); err != nil || string(again) != string(body) {
		t.Fatalf("normalization is not stable: %s, %v", again, err)
	}
}

// The mock resource enforces the schema at the actual outbound HTTP boundary,
// including the namespace introduced after callers have supplied their tools.
func TestToolRequestsIncludeNamespaceDescriptionOnWire(t *testing.T) {
	for _, mode := range []string{"responses", "responses-stream", "chat", "chat-stream"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != resource+"/responses" || r.Header.Get("Authorization") != "Bearer test-access" {
					t.Errorf("unexpected inference request: %s", r.URL)
				}
				var body struct {
					Store, Stream bool
					Tools         []struct {
						Type, Name, Description string
						Tools                   []struct{ Name, Description string }
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if len(body.Tools) != 1 || strings.TrimSpace(body.Tools[0].Description) == "" {
					return reply(r, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "Missing required parameter: 'tools[0].description'.", "type": "invalid_request_error"}}), nil
				}
				namespace := body.Tools[0]
				if namespace.Type != "namespace" || namespace.Name != "functions" || len(namespace.Tools) != 1 || namespace.Tools[0].Name != "lookup" || namespace.Tools[0].Description != "Look up a value." || body.Store || !body.Stream {
					t.Errorf("incorrect tool adaptation: %+v", body)
				}
				sse := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-test\",\"model\":\"test\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello\"}]}]}}\n\n"
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse)), Request: r}, nil
			})}
			p := newTestProvider(t, t.TempDir(), client)
			seedAccount(t, p)
			if err := p.withState(t.Context(), func(s *state) error {
				a := s.Accounts[s.Active]
				a.Access, a.Expires = "test-access", 1<<62
				s.Accounts[s.Active] = a
				return p.save(s)
			}); err != nil {
				t.Fatal(err)
			}
			raw := json.RawMessage(`{"model":"test","input":"hello","tools":[{"type":"function","name":"lookup","description":"Look up a value.","parameters":{"type":"object"}}]}`)
			request := llmprovider.ChatRequest{Model: "test", Messages: []llmprovider.Message{{Role: llmprovider.RoleUser, Content: "hello"}}, Tools: []llmprovider.Tool{{Type: llmprovider.ToolTypeFunction, Function: llmprovider.FunctionDefinition{Name: "lookup", Description: "Look up a value.", Parameters: map[string]any{"type": "object"}}}}}
			switch mode {
			case "responses":
				if _, err := p.CreateResponse(t.Context(), raw, nil); err != nil {
					t.Fatal(err)
				}
			case "responses-stream":
				stream, err := p.CreateResponseStream(t.Context(), raw, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = stream.Close() }()
				if _, err := stream.Recv(); err != nil {
					t.Fatal(err)
				}
			case "chat":
				if response, err := p.Chat(t.Context(), request); err != nil || response.Choices[0].Message.Content != "Hello" {
					t.Fatalf("chat = %+v, %v", response, err)
				}
			case "chat-stream":
				stream, err := p.ChatStream(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = stream.Close() }()
				for {
					if _, err := stream.Recv(); err == io.EOF {
						break
					} else if err != nil {
						t.Fatal(err)
					}
				}
			}
			if calls != 1 {
				t.Fatalf("inference calls = %d, want 1", calls)
			}
		})
	}
}
