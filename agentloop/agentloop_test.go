package agentloop_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
)

func TestConsumeChatStreamRetainsResponsesReplayItems(t *testing.T) {
	output := json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)
	configured := &scriptedClient{stream: &chunkStream{chunks: []*client.ChatChunk{{
		Choices: []client.Choice{{Delta: &client.Message{Role: client.RoleAssistant, Phase: "final_answer", Content: "answer", ResponseOutput: []json.RawMessage{output}, ResponseModel: "native/model"}}},
	}}}}
	response, _, err := agentloop.ConsumeChatStream(t.Context(), configured, client.ChatRequest{Model: "native/model"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	message := response.Choices[0].Message
	if message.Content != "answer" || message.Phase != "final_answer" || len(message.ResponseOutput) != 1 || message.ResponseModel != "native/model" || string(message.ResponseOutput[0]) != string(output) {
		t.Fatalf("streamed response = %#v", message)
	}
}

func TestResponsesStreamingToolRoundThroughAgentLoop(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		bodies = append(bodies, body)
		writer.Header().Set("Content-Type", "text/event-stream")
		if len(bodies) == 1 {
			_, _ = io.WriteString(writer, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"native/model\",\"status\":\"completed\",\"output\":[{\"type\":\"reasoning\",\"encrypted_content\":\"opaque\"},{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"lookup\",\"arguments\":\"{}\"}]}}\n\n")
		} else {
			_, _ = io.WriteString(writer, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"model\":\"native/model\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}]}}\n\n")
		}
	}))
	t.Cleanup(server.Close)
	configured, err := client.New(client.Config{BaseURL: server.URL + "/v1", APIKey: "test", ModelAPIModes: map[string]string{"native/model": "responses"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configured.Close() })
	runtime := &scriptedTools{name: "lookup", content: "found"}
	events := make(chan agentloop.Event)
	go agentloop.RunAgentLoop(t.Context(), agentloop.Request{
		Client: configured, Tools: runtime, Model: "native/model", Stream: true,
		Messages: []client.Message{{Role: client.RoleUser, Content: "look up"}},
	}, events)
	var final agentloop.Result
	for event := range events {
		if err := event.Err(); err != nil {
			t.Fatal(err)
		}
		if result, ok := event.Result(); ok {
			final = result
		}
	}
	if len(runtime.calls) != 1 || len(bodies) != 2 || final.Response == nil || final.Response.Choices[0].Message.Content != "done" {
		t.Fatalf("tool calls = %d, requests = %d, result = %#v", len(runtime.calls), len(bodies), final)
	}
	input := bodies[1]["input"].([]any)
	var reasoning, result bool
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if item["encrypted_content"] == "opaque" {
			reasoning = true
		}
		if item["type"] == "function_call_output" && item["call_id"] == "call_1" && item["output"] == "found" {
			result = true
		}
	}
	if !reasoning || !result || bodies[0]["store"] != false || bodies[1]["store"] != false {
		t.Fatalf("Responses replay lost provider state or tool output: %#v", input)
	}
}

type scriptedClient struct {
	requests []client.ChatRequest
	chat     func(client.ChatRequest) *client.ChatResponse
	stream   client.Stream
}

func (c *scriptedClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	c.requests = append(c.requests, request)
	return c.chat(request), nil
}
func (c *scriptedClient) ChatStream(_ context.Context, request client.ChatRequest) (client.Stream, error) {
	c.requests = append(c.requests, request)
	return c.stream, nil
}
func (*scriptedClient) ListModels(context.Context) ([]client.Model, error) { return nil, nil }
func (*scriptedClient) Close() error                                       { return nil }

type scriptedTools struct {
	name    string
	content string
	calls   []client.ToolCall
}

func (r *scriptedTools) Tools() []client.Tool {
	if r.name == "" {
		return nil
	}
	return []client.Tool{{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
		Name: r.name, Parameters: map[string]any{"type": "object"},
	}}}
}
func (*scriptedTools) Environment() qtools.HostEnvironment {
	return qtools.HostEnvironment{OS: "test", Architecture: "test", Shell: "test"}
}
func (r *scriptedTools) Call(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
	r.calls = append(r.calls, call)
	return client.ToolResult{Content: r.content}, nil
}

var _ agentloop.ChatClient = (*scriptedClient)(nil)
var _ agentloop.ToolRuntime = (*scriptedTools)(nil)

func toolResponse(name string) *client.ChatResponse {
	return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
		Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
			ID: "call-1", Type: client.ToolTypeFunction,
			Function: client.FunctionCall{Name: name, Arguments: `{}`},
		}},
	}}}}
}

func toolResponseWithArguments(name, arguments string) *client.ChatResponse {
	response := toolResponse(name)
	response.Choices[0].Message.ToolCalls[0].Function.Arguments = arguments
	return response
}

func TestDelegationModeRejectsSuccessUntilWorkToolSucceeds(t *testing.T) {
	responses := []*client.ChatResponse{
		toolResponseWithArguments("task_start", `{"objective":"inspect project"}`),
		toolResponseWithArguments("task_complete", `{"outcome":"succeeded","summary":"read files"}`),
		toolResponseWithArguments("delegate_list", `{}`),
		toolResponseWithArguments("task_complete", `{"outcome":"succeeded","summary":"read files"}`),
		toolResponseWithArguments("delegate", `{"subagent_name":"builtin/scout","prompt":"inspect project"}`),
		toolResponseWithArguments("task_complete", `{"outcome":"succeeded","summary":"inspected project"}`),
		finalResponse("done"),
	}
	configured := &scriptedClient{}
	configured.chat = func(client.ChatRequest) *client.ChatResponse {
		index := len(configured.requests) - 1
		if index >= len(responses) {
			t.Fatalf("unexpected model request %d", index)
		}
		return responses[index]
	}
	runtime := &scriptedTools{name: "delegate", content: `{"summary":"inspected"}`}
	events := make(chan agentloop.Event)
	go agentloop.RunAgentLoop(t.Context(), agentloop.Request{
		Client: configured, Tools: runtime, Model: "test", RequireTaskAction: true,
		Messages: []client.Message{{Role: client.RoleUser, Content: "Inspect project"}},
	}, events)
	var rejected, completed int
	for event := range events {
		if err := event.Err(); err != nil {
			t.Fatal(err)
		}
		if message, ok := event.Message(); ok && message.Name == "task_complete" {
			if event.MessageIsToolError() && strings.Contains(message.Content, "requires a successful Q delegate") {
				rejected++
			} else if !event.MessageIsToolError() {
				completed++
			}
		}
	}
	if rejected != 2 || completed != 1 || len(configured.requests) != len(responses) {
		t.Fatalf("rejected=%d completed=%d requests=%d", rejected, completed, len(configured.requests))
	}
}

func TestDelegationModeAllowsHonestBlockedCompletion(t *testing.T) {
	responses := []*client.ChatResponse{
		toolResponseWithArguments("task_start", `{"objective":"inspect project"}`),
		toolResponseWithArguments("task_complete", `{"outcome":"blocked","summary":"cannot inspect","blocker":"no available agent"}`),
		finalResponse("done"),
	}
	configured := &scriptedClient{}
	configured.chat = func(client.ChatRequest) *client.ChatResponse { return responses[len(configured.requests)-1] }
	events := make(chan agentloop.Event)
	go agentloop.RunAgentLoop(t.Context(), agentloop.Request{
		Client: configured, Tools: &scriptedTools{}, Model: "test", RequireTaskAction: true,
		Messages: []client.Message{{Role: client.RoleUser, Content: "Inspect project"}},
	}, events)
	var outcome string
	for event := range events {
		if err := event.Err(); err != nil {
			t.Fatal(err)
		}
		if result, ok := event.Result(); ok {
			outcome = result.Outcome
		}
	}
	if outcome != "blocked" || len(configured.requests) != len(responses) {
		t.Fatalf("outcome=%q requests=%d", outcome, len(configured.requests))
	}
}

func TestDelegationModeAcceptsRecordedWorkAfterResume(t *testing.T) {
	responses := []*client.ChatResponse{
		toolResponseWithArguments("task_complete", `{"outcome":"succeeded","summary":"inspected project"}`),
		finalResponse("done"),
	}
	configured := &scriptedClient{}
	configured.chat = func(client.ChatRequest) *client.ChatResponse { return responses[len(configured.requests)-1] }
	events := make(chan agentloop.Event)
	go agentloop.RunAgentLoop(t.Context(), agentloop.Request{
		Client: configured, Tools: &scriptedTools{}, Model: "test", RequireTaskAction: true, PriorTaskAction: true,
		ActiveTask: &workspace.ActiveTask{Objective: "inspect project"},
		Messages:   []client.Message{{Role: client.RoleUser, Content: "Inspect project"}},
	}, events)
	var outcome string
	for event := range events {
		if err := event.Err(); err != nil {
			t.Fatal(err)
		}
		if result, ok := event.Result(); ok {
			outcome = result.Outcome
		}
	}
	if outcome != "succeeded" || len(configured.requests) != len(responses) {
		t.Fatalf("outcome=%q requests=%d", outcome, len(configured.requests))
	}
}

func finalResponse(content string) *client.ChatResponse {
	return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, Content: content}}}}
}

func TestWorkspaceAndToolRound(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Keep changes focused."), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime := &scriptedTools{name: "echo", content: `{"text":"hello"}`}
	base := []client.Message{{Role: client.RoleSystem, Content: "Host instruction"}}
	messages := agentloop.PrepareWorkspaceMessages(base, agentloop.WorkspaceMessageOptions{Root: root, Tools: runtime})
	if len(base) != 1 || !strings.Contains(joined(messages), "Keep changes focused.") || !strings.Contains(joined(messages), root) {
		t.Fatalf("workspace preparation: %#v", messages)
	}
	messages = append(messages, client.Message{Role: client.RoleUser, Content: "echo"})
	configured := &scriptedClient{chat: func(request client.ChatRequest) *client.ChatResponse {
		if len(request.Messages) > len(messages) {
			return finalResponse("done")
		}
		return toolResponse("echo")
	}}
	events := make(chan agentloop.Event)
	go agentloop.RunAgentLoop(t.Context(), agentloop.Request{
		Client: configured, Tools: runtime, Model: "test", Messages: messages, WorkingDirectory: root,
	}, events)
	var calls, results int
	var final agentloop.Result
	for event := range events {
		if err := event.Err(); err != nil {
			t.Fatal(err)
		}
		if _, ok := event.ToolCall(); ok {
			calls++
		}
		if message, ok := event.Message(); ok && message.Role == client.RoleTool {
			results++
		}
		if result, ok := event.Result(); ok {
			final = result
		}
	}
	if calls != 1 || results != 1 || len(runtime.calls) != 1 || final.ToolCalls != 1 || final.Response.Choices[0].Message.Content != "done" {
		t.Fatalf("calls=%d results=%d runtime=%d final=%#v", calls, results, len(runtime.calls), final)
	}
}

func TestQuestionCanResumeThroughPublicEvent(t *testing.T) {
	configured := &scriptedClient{chat: func(request client.ChatRequest) *client.ChatResponse {
		if len(request.Messages) > 1 {
			return finalResponse("answered")
		}
		return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
			Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
				ID: "question-1", Type: client.ToolTypeFunction,
				Function: client.FunctionCall{Name: "ask_to_user", Arguments: `{"question":"Which?","choices":[{"id":"a","label":"A"}]}`},
			}},
		}}}}
	}}
	events := make(chan agentloop.Event)
	go agentloop.RunAgentLoop(t.Context(), agentloop.Request{
		Client: configured, Tools: &scriptedTools{}, Model: "test",
		Messages: []client.Message{{Role: client.RoleUser, Content: "Ask me"}},
	}, events)
	questions := 0
	for event := range events {
		if err := event.Err(); err != nil {
			t.Fatal(err)
		}
		if question, answer, ok := event.Question(); ok {
			questions++
			if question.Question != "Which?" || len(question.Choices) != 1 {
				t.Fatalf("question = %#v", question)
			}
			answer <- agentloop.AgentAnswer{SelectedChoiceID: "a"}
		}
		if result, ok := event.Result(); ok && result.Response.Choices[0].Message.Content != "answered" {
			t.Fatalf("result = %#v", result)
		}
	}
	if questions != 1 || len(configured.requests) != 2 || !strings.Contains(joined(configured.requests[1].Messages), `"selected_choice_id":"a"`) {
		t.Fatalf("question resume: questions=%d requests=%#v", questions, configured.requests)
	}
}

func TestCompactionResumesWithCheckpoint(t *testing.T) {
	configured := &scriptedClient{}
	configured.chat = func(request client.ChatRequest) *client.ChatResponse {
		if len(request.Tools) == 0 && strings.Contains(joined(request.Messages), "session continuation checkpoint") {
			body, _ := json.Marshal(memory.Checkpoint{ActiveWork: []string{"condensed evidence"}})
			return finalResponse(string(body))
		}
		if len(configured.requests) == 1 {
			return toolResponse("large_read")
		}
		return finalResponse("done")
	}
	runtime := &scriptedTools{name: "large_read", content: strings.Repeat("large result ", 5000)}
	events := make(chan agentloop.Event)
	go agentloop.RunAgentLoop(t.Context(), agentloop.Request{
		Client: configured, Tools: runtime, Model: "test",
		Messages:      []client.Message{{Role: client.RoleSystem, Content: "system"}, {Role: client.RoleUser, Content: "read"}},
		ContextPolicy: memory.Policy{ContextWindow: 16000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07},
	}, events)
	compactions := 0
	for event := range events {
		if err := event.Err(); err != nil {
			t.Fatal(err)
		}
		if compaction, ok := event.Compaction(); ok {
			compactions++
			if !strings.Contains(compaction.Summary, "condensed evidence") {
				t.Fatalf("summary = %q", compaction.Summary)
			}
		}
	}
	if compactions != 1 || len(configured.requests) != 3 || configured.requests[2].ConversationID != "" || !strings.Contains(joined(configured.requests[2].Messages), "condensed evidence") {
		t.Fatalf("compactions=%d requests=%#v", compactions, configured.requests)
	}
}

func TestCompactionPreservesActiveTaskStartExchange(t *testing.T) {
	configured := &scriptedClient{chat: func(request client.ChatRequest) *client.ChatResponse {
		body, _ := json.Marshal(memory.Checkpoint{ActiveWork: []string{"continue active task"}})
		return finalResponse(string(body))
	}}
	policy := memory.Policy{ContextWindow: 16_000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}
	context := agentloop.NewContext(policy, []client.Message{
		{Role: client.RoleSystem, Content: "system"},
		{Role: client.RoleUser, Content: "inspect"},
	}, nil)
	start := client.ToolCall{ID: "start", Type: client.ToolTypeFunction, Function: client.FunctionCall{
		Name: "task_start", Arguments: `{"objective":"inspect"}`,
	}}
	context.Append(
		client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{start}},
		client.ToolResultMessage(start, client.ToolResult{Content: `{"started":true}`}),
		client.Message{Role: client.RoleAssistant, Content: strings.Repeat("old work ", 8_000)},
	)
	compaction, err := context.CompactIfNeeded(t.Context(), configured, "test", "")
	if err != nil || compaction == nil {
		t.Fatalf("compaction=%#v err=%v", compaction, err)
	}
	var callKept, resultKept bool
	for _, message := range context.Messages() {
		for _, call := range message.ToolCalls {
			callKept = callKept || call.Function.Name == "task_start" && call.ID == start.ID
		}
		resultKept = resultKept || message.Role == client.RoleTool && message.Name == "task_start" && message.ToolCallID == start.ID
	}
	if !callKept || !resultKept {
		t.Fatalf("task_start exchange was compacted: %#v", context.Messages())
	}
}

type chunkStream struct {
	chunks []*client.ChatChunk
	index  int
}

func (s *chunkStream) Recv() (*client.ChatChunk, error) {
	if s.index == len(s.chunks) {
		return nil, io.EOF
	}
	chunk := s.chunks[s.index]
	s.index++
	return chunk, nil
}
func (*chunkStream) Close() error { return nil }

func TestStreamingEmitsResponseDelta(t *testing.T) {
	configured := &scriptedClient{stream: &chunkStream{chunks: []*client.ChatChunk{{
		Choices: []client.Choice{{Delta: &client.Message{Role: client.RoleAssistant, Content: "streamed"}, FinishReason: "stop"}},
	}}}}
	events := make(chan agentloop.Event)
	go agentloop.RunAgentLoop(t.Context(), agentloop.Request{
		Client: configured, Tools: &scriptedTools{}, Model: "test", Stream: true,
		Messages: []client.Message{{Role: client.RoleUser, Content: "respond"}},
	}, events)
	var delta string
	var final string
	for event := range events {
		if err := event.Err(); err != nil {
			t.Fatal(err)
		}
		if part, ok := event.StreamDelta(); ok && part.Kind == agentloop.AgentStreamResponse {
			delta += part.Content
		}
		if result, ok := event.Result(); ok {
			final = result.Response.Choices[0].Message.Content
		}
	}
	if delta != "streamed" || final != "streamed" {
		t.Fatalf("delta=%q final=%q", delta, final)
	}
}

func joined(messages []client.Message) string {
	var values []string
	for _, message := range messages {
		values = append(values, message.TextContent())
	}
	return strings.Join(values, "\n")
}
