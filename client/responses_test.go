package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesProviderPreferenceAndExactChatOverride(t *testing.T) {
	configured := &Client{
		modelAPIModes:    map[string]string{"openai/chat-only": "chat_completions", "local/manual": "responses"},
		providerAPIModes: map[string]string{"openai": "responses"},
	}
	for model, expected := range map[string]string{
		"openai/gpt-5-nano": "responses",
		"openai/chat-only":  "chat_completions",
		"local/manual":      "responses",
		"codex/gpt-6-sol":   "chat_completions",
		"group/multi":       "chat_completions",
	} {
		if mode := configured.apiMode(model); mode != expected {
			t.Fatalf("%s mode = %q, want %q", model, mode, expected)
		}
	}
}

func TestResponsesModeReplaysNativeOutputAndToolResult(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/responses" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("X-Q-Require-Native-Responses") != "true" {
			t.Errorf("native requirement header missing")
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests = append(requests, body)
		writer.Header().Set("Content-Type", "application/json")
		if len(requests) == 1 {
			_, _ = io.WriteString(writer, `{"id":"resp_1","model":"native/model","status":"completed","output":[{"type":"reasoning","id":"rs_1","encrypted_content":"opaque"},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"q\":1}"}],"usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110,"input_tokens_details":{"cached_tokens":50}}}`)
		} else {
			_, _ = io.WriteString(writer, `{"id":"resp_2","model":"native/model","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":120,"output_tokens":2,"total_tokens":122}}`)
		}
	}))
	defer server.Close()
	configured, err := New(Config{BaseURL: server.URL + "/v1", APIKey: "test", DefaultModel: "native/model", ModelAPIModes: map[string]string{"native/model": "responses"}, ForwardUsageMetadata: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configured.Close() })
	user := Message{Role: RoleUser, ContentParts: []MessageContentPart{{"type": "text", "text": "see image"}, {"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AA=="}}}}
	first, err := configured.Chat(t.Context(), ChatRequest{Messages: []Message{user}, Tools: []Tool{{Type: ToolTypeFunction, Function: FunctionDefinition{Name: "lookup", Parameters: map[string]any{"type": "object"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	assistant := first.Choices[0].Message
	if len(assistant.ResponseOutput) != 2 || assistant.ResponseModel != "native/model" || len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "call_1" || first.Usage.PromptDetails.CachedTokens != 50 {
		t.Fatalf("first response = %#v", first)
	}
	second, err := configured.Chat(t.Context(), ChatRequest{Messages: []Message{user, assistant, {Role: RoleTool, ToolCallID: "call_1", Content: "result"}}, ConversationID: first.ConversationID})
	if err != nil {
		t.Fatal(err)
	}
	if second.Choices[0].Message.Content != "done" || second.ConversationID != first.ConversationID {
		t.Fatalf("second response = %#v", second)
	}
	if len(requests) != 2 || requests[0]["store"] != false || requests[0]["cache_affinity_id"] != requests[1]["cache_affinity_id"] {
		t.Fatalf("request affinity = %#v", requests)
	}
	firstInput := requests[0]["input"].([]any)
	content := firstInput[0].(map[string]any)["content"].([]any)
	if content[1].(map[string]any)["type"] != "input_image" || content[1].(map[string]any)["image_url"] != "data:image/png;base64,AA==" {
		t.Fatalf("image input = %#v", content)
	}
	secondInput := requests[1]["input"].([]any)
	if len(secondInput) != 4 || secondInput[1].(map[string]any)["encrypted_content"] != "opaque" || secondInput[2].(map[string]any)["call_id"] != "call_1" || secondInput[3].(map[string]any)["type"] != "function_call_output" {
		t.Fatalf("replayed input = %#v", secondInput)
	}
}

func TestResponsesModelChangeRebuildsVisibleHistory(t *testing.T) {
	previous := Message{
		Role: RoleAssistant, Content: "visible answer", ResponseModel: "first/model",
		ResponseOutput: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"first-model-secret"}`)},
	}
	input, err := responseInput([]Message{previous}, "second/model")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "first-model-secret") || !strings.Contains(string(encoded), "visible answer") {
		t.Fatalf("cross-model replay = %s", encoded)
	}
	if input[0].(map[string]any)["phase"] != "final_answer" {
		t.Fatalf("cross-model assistant phase = %#v", input[0])
	}
}

func TestResponsesOutputSchemaUsesTextFormat(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}, "required": []string{"answer"}, "additionalProperties": false}
	body, err := (&Client{}).responseRequest(ChatRequest{Model: "native/model", Messages: []Message{{Role: RoleUser, Content: "Reply as JSON."}}, OutputSchema: schema}, "cache_test")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	format := fields["text"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["name"] != "q_output" || format["strict"] != true || format["schema"].(map[string]any)["type"] != "object" {
		t.Fatalf("Responses structured output = %#v", format)
	}
}

func TestResponsesMessagePhaseSeparatesCommentaryFromFinalAnswer(t *testing.T) {
	const reply = `{"id":"resp_phase","model":"native/model","status":"completed","output":[{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"I will check."}]},{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"The answer is four."}]}]}`
	parsed, err := parseResponseDocument(json.RawMessage(reply), "cache_test", "native/model")
	if err != nil {
		t.Fatal(err)
	}
	message := parsed.Choices[0].Message
	if message.Content != "The answer is four." || len(message.ResponseOutput) != 2 {
		t.Fatalf("phased reply = %#v", message)
	}
	input, err := responseInput([]Message{message}, "native/model")
	if err != nil {
		t.Fatal(err)
	}
	if len(input) != 2 || input[0].(map[string]any)["phase"] != "commentary" || input[1].(map[string]any)["phase"] != "final_answer" {
		t.Fatalf("phased replay = %#v", input)
	}
}

func TestResponsesStreamMessagePhaseSeparatesCommentaryFromFinalAnswer(t *testing.T) {
	const reply = `{"id":"resp_phase","model":"native/model","status":"completed","output":[{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"I will check."}]},{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"The answer is four."}]}]}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"phase\":\"commentary\"}}\n\n")
		_, _ = io.WriteString(writer, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"I will check.\"}\n\n")
		_, _ = io.WriteString(writer, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"message\",\"phase\":\"final_answer\"}}\n\n")
		_, _ = io.WriteString(writer, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":1,\"delta\":\"The answer is four.\"}\n\n")
		_, _ = io.WriteString(writer, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":"+reply+"}\n\n")
	}))
	defer server.Close()
	configured, err := New(Config{BaseURL: server.URL + "/v1", APIKey: "test", ModelAPIModes: map[string]string{"native/model": "responses"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configured.Close() })
	stream, err := configured.ChatStream(t.Context(), ChatRequest{Model: "native/model", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	var final string
	var commentary string
	var completionPhase string
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta == nil {
				continue
			}
			if len(choice.Delta.ResponseOutput) > 0 {
				completionPhase = choice.Delta.Phase
			}
			if choice.Phase == "commentary" {
				commentary += choice.Delta.Content
			} else {
				final += choice.Delta.Content
			}
		}
	}
	if commentary != "I will check." || final != "The answer is four." || completionPhase != "final_answer" {
		t.Fatalf("streamed phases: commentary=%q final=%q completion=%q", commentary, final, completionPhase)
	}
}

func TestResponsesStreamNeedsCompletionAndPreservesOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"thinking\"}\n\n")
		_, _ = io.WriteString(writer, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
		_, _ = io.WriteString(writer, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"native/model\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n")
	}))
	defer server.Close()
	configured, err := New(Config{BaseURL: server.URL + "/v1", APIKey: "test", ModelAPIModes: map[string]string{"native/model": "responses"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configured.Close() })
	stream, err := configured.ChatStream(t.Context(), ChatRequest{Model: "native/model", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	var chunks []*ChatChunk
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) != 3 || chunks[0].Choices[0].Phase != "commentary" || chunks[1].Choices[0].Delta.Content != "hello" || len(chunks[2].Choices[0].Delta.ResponseOutput) != 1 || chunks[2].Choices[0].Delta.ResponseModel != "native/model" || chunks[2].Usage.PromptTokens != 10 {
		t.Fatalf("chunks = %#v", chunks)
	}
}

func TestResponsesStreamRejectsMissingCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
	}))
	defer server.Close()
	configured, err := New(Config{BaseURL: server.URL + "/v1", APIKey: "test", ModelAPIModes: map[string]string{"native/model": "responses"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configured.Close() })
	stream, err := configured.ChatStream(context.Background(), ChatRequest{Model: "native/model", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	_, _ = stream.Recv()
	if _, err := stream.Recv(); err == nil || !strings.Contains(err.Error(), "without response.completed") {
		t.Fatalf("missing completion error = %v", err)
	}
}
