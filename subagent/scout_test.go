package subagent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/sessionstore"
)

type fakeScoutClient struct {
	responses []client.Message
	requests  []client.ChatRequest
}

func (f *fakeScoutClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	f.requests = append(f.requests, request)
	if len(f.responses) == 0 {
		return nil, errors.New("no scout response")
	}
	message := f.responses[0]
	f.responses = f.responses[1:]
	return &client.ChatResponse{
		Choices: []client.Choice{{Message: message}},
		Usage:   client.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
	}, nil
}

type fakeScoutTools struct {
	available []client.Tool
	calls     []client.ToolCall
}

func (f *fakeScoutTools) Tools() []client.Tool { return append([]client.Tool(nil), f.available...) }

func (f *fakeScoutTools) Call(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
	f.calls = append(f.calls, call)
	return client.ToolResult{Content: `{"path":"subagent/scout.go","content":"package subagent"}`}, nil
}

type scoutRecordSink struct{ records []sessionstore.Record }

func (s *scoutRecordSink) Append(record sessionstore.Record) error {
	s.records = append(s.records, record)
	return nil
}

func TestScoutRunnerUsesReadOnlyToolsAndReturnsStructuredReport(t *testing.T) {
	complete := `{
		"outcome":"succeeded",
		"summary":"Located the scout execution boundary",
		"findings":[{
			"path":"subagent/scout.go",
			"symbol":"ScoutRunner.Run",
			"summary":"The runner owns an isolated tool loop",
			"evidence":["ScoutRunner filters the runtime tool catalog"]
		}],
		"verification":["Read subagent/scout.go"]
	}`
	fakeClient := &fakeScoutClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall("read_file", `{"path":"subagent/scout.go"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(ScoutCompleteToolName, complete)}},
	}}
	fakeTools := &fakeScoutTools{available: []client.Tool{
		scoutFunctionTool("edit_file"), scoutFunctionTool("loom_eval"), scoutFunctionTool("read_file"),
	}}
	sink := &scoutRecordSink{}
	var progress []ProgressEvent
	ref := loom.Ref("loom://0123456789abcdef0123456789abcdef")
	runner := ScoutRunner{
		Client: fakeClient, Tools: fakeTools,
		Spec: Spec{Role: config.AgentRoleScout, Model: "scout-model", ReasoningEffort: "medium"},
		Sink: sink, RunID: "run-plan-1", WorkingDirectory: `C:\workspace`,
		Progress: func(event ProgressEvent) {
			progress = append(progress, event)
		},
	}
	result, err := runner.Run(context.Background(), ScoutTask{
		Objective:  "Find where a scout should connect to the plan flow",
		LoomInputs: []loom.Ref{ref},
		Context:    []string{"The Griller needs repository evidence before asking another question."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "succeeded" || len(result.Findings) != 1 || result.Findings[0].Path != "subagent/scout.go" {
		t.Fatalf("result = %#v", result)
	}
	if result.Usage.TotalTokens != 24 {
		t.Fatalf("usage = %#v", result.Usage)
	}
	if len(fakeClient.requests) != 2 || fakeClient.requests[0].Model != "scout-model" ||
		fakeClient.requests[0].ReasoningEffort != "medium" || fakeClient.requests[0].WorkingDirectory != `C:\workspace` {
		t.Fatalf("requests = %#v", fakeClient.requests)
	}
	for _, tool := range fakeClient.requests[0].Tools {
		if tool.Function.Name == "edit_file" {
			t.Fatal("scout was offered edit_file")
		}
	}
	if !hasScoutTool(fakeClient.requests[0].Tools, "read_file") ||
		!hasScoutTool(fakeClient.requests[0].Tools, "loom_eval") ||
		!hasScoutTool(fakeClient.requests[0].Tools, ScoutCompleteToolName) {
		t.Fatalf("scout tools = %#v", fakeClient.requests[0].Tools)
	}
	if len(fakeTools.calls) != 1 || fakeTools.calls[0].Function.Name != "read_file" {
		t.Fatalf("tool calls = %#v", fakeTools.calls)
	}
	if !strings.Contains(fakeClient.requests[0].Messages[0].Content, "return precise evidence to the Griller") ||
		!strings.Contains(fakeClient.requests[0].Messages[1].Content, ref.String()) {
		t.Fatalf("messages = %#v", fakeClient.requests[0].Messages)
	}
	if len(sink.records) < 5 || sink.records[len(sink.records)-1].Status != sessionstore.StatusSucceeded {
		t.Fatalf("lifecycle records = %#v", sink.records)
	}
	if len(progress) < 6 || progress[0].Action != ProgressStarted ||
		progress[len(progress)-1].Action != ProgressCompleted {
		t.Fatalf("progress events = %#v", progress)
	}
	var sawRead, sawComplete bool
	for _, event := range progress {
		if event.Agent != "scout" || event.TaskID == "" {
			t.Fatalf("unidentified Scout progress = %#v", event)
		}
		if event.Action == ProgressTool && event.Detail == "read_file" {
			sawRead = true
		}
		if event.Action == ProgressTool && event.Detail == ScoutCompleteToolName {
			sawComplete = true
		}
	}
	if !sawRead || !sawComplete {
		t.Fatalf("tool progress missing: %#v", progress)
	}
}

func TestScoutRunnerRejectsUnexposedMutationAndRemindsCompletion(t *testing.T) {
	fakeClient := &fakeScoutClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall("edit_file", `{}`)}},
		{Role: client.RoleAssistant, Content: "plain report"},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(ScoutCompleteToolName,
			`{"outcome":"blocked","summary":"No readable evidence","blocker":"The requested file is unavailable"}`)}},
	}}
	fakeTools := &fakeScoutTools{available: []client.Tool{scoutFunctionTool("edit_file"), scoutFunctionTool("read_file")}}
	runner := ScoutRunner{
		Client: fakeClient, Tools: fakeTools,
		Spec: Spec{Role: config.AgentRoleScout, Model: "scout-model"},
	}
	result, err := runner.Run(context.Background(), ScoutTask{Objective: "Inspect the missing implementation"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "blocked" || result.Blocker == "" || len(fakeTools.calls) != 0 {
		t.Fatalf("result = %#v, calls = %#v", result, fakeTools.calls)
	}
	if len(fakeClient.requests) != 3 {
		t.Fatalf("requests = %d", len(fakeClient.requests))
	}
	choice, ok := fakeClient.requests[2].ToolChoice.(map[string]any)
	if !ok || choice["type"] != "function" {
		t.Fatalf("reminder tool choice = %#v", fakeClient.requests[2].ToolChoice)
	}
}

func TestScoutCompletionValidation(t *testing.T) {
	if _, err := parseScoutCompletion(`{"outcome":"blocked","summary":"Missing context"}`); err == nil {
		t.Fatal("blocked scout completion without blocker was accepted")
	}
	if _, err := normalizeScoutTask(ScoutTask{
		Objective: "Inspect", LoomInputs: []loom.Ref{"loom://invalid"},
	}); err == nil {
		t.Fatal("invalid Loom input was accepted")
	}
}

func scoutCall(name, arguments string) client.ToolCall {
	return client.ToolCall{
		ID: "call-" + name, Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: name, Arguments: arguments},
	}
}

func scoutFunctionTool(name string) client.Tool {
	return client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
		Name: name, Parameters: map[string]any{"type": "object"},
	}}
}

func hasScoutTool(tools []client.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}
