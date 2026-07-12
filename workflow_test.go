package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadWorkflow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	data := `version: 1
name: test-loop
loop:
  max_iterations: 3
  repair_attempts: 2
  review:
    provider: grok
    prompt: "Review {{ input }}"
  improve:
    provider: codex
    prompt: "Apply {{ review }}"
  verify:
    command: go test ./...
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	workflow, err := LoadWorkflow(path)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Name != "test-loop" || workflow.Loop.MaxIterations != 3 || workflow.Loop.Review.Provider != "grok" {
		t.Fatalf("unexpected workflow: %#v", workflow)
	}
}

func TestLoadWorkflowRejectsUnknownProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	data := `version: 1
name: bad
loop:
  max_iterations: 1
  review: { provider: unknown, prompt: review }
  improve: { provider: codex, prompt: improve }
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkflow(path); err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("expected provider error, got %v", err)
	}
}

func TestExtractStructuredPayload(t *testing.T) {
	payload := `{"done":true,"summary":"clean","issues":[],"plan":[]}`
	tests := []struct{ provider, raw string }{
		{"codex", payload}, {"agy", payload},
		{"grok", `{"structuredOutput":` + payload + `}`},
		{"claude", `{"structured_output":` + payload + `}`},
		{"Codex", payload},
		{"Claude", `{"structured_output":` + payload + `}`},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			got, err := extractStructuredPayload(test.provider, test.raw)
			if err != nil {
				t.Fatal(err)
			}
			var review ReviewOutput
			if err := json.Unmarshal(got, &review); err != nil {
				t.Fatal(err)
			}
			if !review.Done || review.Summary != "clean" {
				t.Fatalf("unexpected review: %#v", review)
			}
		})
	}
}

func TestExecuteCommandPreservesFailureExitCode(t *testing.T) {
	_, _, _, err := executeCommand(GetSystemInfo().Shell, "go test ./package-that-does-not-exist", ".")
	if err == nil {
		t.Fatal("expected command failure")
	}
}

func TestValidateReviewOutput(t *testing.T) {
	valid := &ReviewOutput{Summary: "fix needed", Issues: []ReviewIssue{{ID: "one", Severity: "high", Description: "bug", Evidence: "x.go:1", AcceptanceCriteria: "test passes"}}, Plan: []ReviewPlan{{IssueID: "one", Action: "fix it"}}}
	if err := validateReviewOutput(valid); err != nil {
		t.Fatal(err)
	}
	invalid := *valid
	invalid.Done = true
	if err := validateReviewOutput(&invalid); err == nil {
		t.Fatal("expected done/issues contradiction")
	}
	missingPlan := *valid
	missingPlan.Plan = nil
	if err := validateReviewOutput(&missingPlan); err == nil {
		t.Fatal("expected missing plan error")
	}
}

func TestRenderWorkflowTemplate(t *testing.T) {
	got := renderWorkflowTemplate("task={{ input }} iteration={{iteration}} review={{ review }}", map[string]string{"input": "ship", "iteration": "2", "review": "{}"})
	if got != "task=ship iteration=2 review={}" {
		t.Fatalf("got %q", got)
	}
}

func TestNewWorkflowRunSnapshotsDefinition(t *testing.T) {
	workflow := &Workflow{Version: 1, Name: "snapshot", Loop: WorkflowLoop{MaxIterations: 2}}
	run, err := NewWorkflowRun(workflow, "workflow.yaml", "default", "task")
	if err != nil {
		t.Fatal(err)
	}
	workflow.Name = "changed"
	if run.Definition.Name != "snapshot" || run.Definition.Loop.MaxIterations != 2 {
		t.Fatalf("definition was not snapshotted: %#v", run.Definition)
	}
}
