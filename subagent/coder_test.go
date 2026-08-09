package subagent

import (
	"context"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/sessionstore"
)

func TestCoderRunnerUsesWorkspaceToolsAndCompletesAttempt(t *testing.T) {
	fakeClient := &fakeScoutClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall("edit_file", `{"path":"app/model.go"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(CoderCompleteToolName, `{
			"outcome":"succeeded",
			"summary":"Updated the model",
			"artifacts":["app/model.go"],
			"verification":["go test ./app"]
		}`)}},
	}}
	fakeTools := &fakeScoutTools{available: []client.Tool{
		scoutFunctionTool("read_file"), scoutFunctionTool("edit_file"), scoutFunctionTool("run_command"),
		scoutFunctionTool(AskToUserToolName), scoutFunctionTool(DelegateScoutToolName),
	}}
	sink := &scoutRecordSink{}
	var progress []ProgressEvent
	runner := CoderRunner{
		Client: fakeClient, Tools: fakeTools,
		Spec: Spec{Role: config.AgentRoleCoder, Model: "coder-model", ReasoningEffort: "high"},
		Sink: sink, RunID: "run-1", ExecutionID: "execution-1",
		WorkingDirectory: `C:\workspace`, Environment: "Runtime environment: Windows and PowerShell.",
		Progress: func(event ProgressEvent) { progress = append(progress, event) },
	}
	result, err := runner.Run(context.Background(), CoderAttempt{
		Plan: executableTestPlan(), TaskIndex: 0, Attempt: 1,
		Targets: []string{"app/model.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "succeeded" || result.Summary != "Updated the model" || len(result.Verification) != 1 {
		t.Fatalf("Coder result = %#v", result)
	}
	if len(fakeTools.calls) != 1 || fakeTools.calls[0].Function.Name != "edit_file" {
		t.Fatalf("workspace calls = %#v", fakeTools.calls)
	}
	if len(fakeClient.requests) != 2 || fakeClient.requests[0].Model != "coder-model" ||
		fakeClient.requests[0].ReasoningEffort != "high" || fakeClient.requests[0].WorkingDirectory != `C:\workspace` {
		t.Fatalf("Coder requests = %#v", fakeClient.requests)
	}
	for _, expected := range []string{"read_file", "edit_file", "run_command", CoderCompleteToolName} {
		if !hasScoutTool(fakeClient.requests[0].Tools, expected) {
			t.Fatalf("Coder tool %q missing: %#v", expected, fakeClient.requests[0].Tools)
		}
	}
	if hasScoutTool(fakeClient.requests[0].Tools, AskToUserToolName) ||
		hasScoutTool(fakeClient.requests[0].Tools, DelegateScoutToolName) {
		t.Fatalf("Coder was offered orchestration tools: %#v", fakeClient.requests[0].Tools)
	}
	prompt := fakeClient.requests[0].Messages[0].Content
	for _, expected := range []string{`"plan"`, `"resolved_targets"`, "app/model.go", "Runtime environment: Windows"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("Coder prompt does not contain %q:\n%s", expected, prompt)
		}
	}
	if len(sink.records) < 5 || sink.records[len(sink.records)-1].Status != sessionstore.StatusSucceeded {
		t.Fatalf("Coder lifecycle records = %#v", sink.records)
	}
	var sawEdit, sawComplete bool
	for _, event := range progress {
		if event.Agent != "coder" || event.TaskID != "execution-1-coder-1-1" {
			t.Fatalf("Coder progress identity = %#v", event)
		}
		if event.Action == ProgressTool && event.Detail == "edit_file" {
			sawEdit = true
		}
		if event.Action == ProgressTool && event.Detail == CoderCompleteToolName {
			sawComplete = true
		}
	}
	if !sawEdit || !sawComplete {
		t.Fatalf("Coder tool progress = %#v", progress)
	}
}

func TestCoderCompletionRequiresBlocker(t *testing.T) {
	if _, err := parseCoderCompletion(`{"outcome":"blocked","summary":"Cannot continue"}`); err == nil {
		t.Fatal("blocked Coder completion without blocker was accepted")
	}
}
