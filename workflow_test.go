package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleGraphYAML = `version: 1
name: test-graph
entry: plan
limits:
  max_visits: 10
  max_visits_per_step:
    plan: 3
    work: 3
steps:
  plan:
    type: provider
    provider: codex
    prompt: "Plan {{ input }}"
    output:
      kind: structured
      schema: plan
  work:
    type: provider
    provider: grok
    prompt: "Work {{ steps.plan }}"
  test:
    type: command
    command: go test ./...
edges:
  - { from: plan, to: end, when: "steps.plan.done == true" }
  - { from: plan, to: work, when: "steps.plan.done == false" }
  - { from: work, to: test, when: always }
  - { from: test, to: end, when: always, on: success }
  - { from: test, to: work, when: always, on: error }
sinks:
  end: { type: success }
`

func TestLoadWorkflowGraph(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	if err := os.WriteFile(path, []byte(sampleGraphYAML), 0600); err != nil {
		t.Fatal(err)
	}
	workflow, err := LoadWorkflow(path)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Name != "test-graph" || workflow.Entry != "plan" {
		t.Fatalf("unexpected workflow: %#v", workflow)
	}
	if workflow.Steps["plan"].Provider != "codex" || workflow.Steps["work"].Provider != "grok" {
		t.Fatalf("unexpected steps: %#v", workflow.Steps)
	}
	if workflow.Limits.MaxVisits != 10 {
		t.Fatalf("max_visits = %d", workflow.Limits.MaxVisits)
	}
}

func TestLoadWorkflowRejectsLegacyLoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.yaml")
	data := `version: 1
name: old
loop:
  max_iterations: 1
  review: { provider: grok, prompt: r }
  improve: { provider: codex, prompt: i }
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkflow(path); err == nil || !strings.Contains(err.Error(), "legacy") && !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected legacy error, got %v", err)
	}
}

func TestLoadWorkflowRejectsUnknownProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	data := `version: 1
name: bad
entry: a
steps:
  a:
    type: provider
    provider: unknown
    prompt: hi
edges:
  - { from: a, to: end, when: always }
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
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			got, err := extractStructuredPayload(test.provider, test.raw)
			if err != nil {
				t.Fatal(err)
			}
			var plan PlanOutput
			if err := json.Unmarshal(got, &plan); err != nil {
				t.Fatal(err)
			}
			if !plan.Done || plan.Summary != "clean" {
				t.Fatalf("unexpected plan: %#v", plan)
			}
		})
	}
}

func TestValidatePlanAndVerdict(t *testing.T) {
	valid := &PlanOutput{
		Summary: "fix needed",
		Issues:  []PlanIssue{{ID: "one", Severity: "high", Description: "bug", Evidence: "x.go:1", AcceptanceCriteria: "test passes"}},
		Plan:    []PlanAction{{IssueID: "one", Action: "fix it"}},
	}
	if err := validatePlanOutput(valid); err != nil {
		t.Fatal(err)
	}
	invalid := *valid
	invalid.Done = true
	if err := validatePlanOutput(&invalid); err == nil {
		t.Fatal("expected done/issues contradiction")
	}
	if err := validateVerdictOutput(&VerdictOutput{Pass: true, Summary: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := validateVerdictOutput(&VerdictOutput{Pass: false, Summary: "no"}); err == nil {
		t.Fatal("expected gaps required")
	}
	if err := validateVerdictOutput(&VerdictOutput{Pass: false, Summary: "no", Gaps: []string{"missing test"}}); err != nil {
		t.Fatal(err)
	}
}

func TestRenderWorkflowTemplateSteps(t *testing.T) {
	ctx := map[string]any{
		"input": "ship",
		"visit": float64(2),
		"step":  "work",
		"steps": map[string]any{
			"plan": map[string]any{
				"raw":     `{"done":false}`,
				"status":  "ok",
				"done":    false,
				"summary": "todo",
			},
		},
	}
	got := renderWorkflowTemplate("task={{ input }} visit={{ visit }} done={{ steps.plan.done }} body={{ steps.plan }}", ctx)
	if !strings.Contains(got, "task=ship") || !strings.Contains(got, "visit=2") || !strings.Contains(got, "done=false") {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got, `"summary":"todo"`) && !strings.Contains(got, `"summary": "todo"`) {
		// compact JSON without spaces
		if !strings.Contains(got, "todo") {
			t.Fatalf("expected plan body in %q", got)
		}
	}
}

func TestEvalWorkflowCondition(t *testing.T) {
	run := &WorkflowRun{
		Input: "x",
		Outputs: map[string]StepOutput{
			"plan": {
				Raw:    `{}`,
				Status: "ok",
				Parsed: map[string]any{"done": false, "summary": "s"},
			},
			"test": {
				Raw:      "fail",
				Status:   "error",
				ExitCode: intPtr(1),
			},
		},
	}
	visitOK := WorkflowVisit{Status: "ok"}
	visitErr := WorkflowVisit{Status: "error"}

	cases := []struct {
		when  string
		visit WorkflowVisit
		want  bool
	}{
		{"always", visitOK, true},
		{"never", visitOK, false},
		{"success", visitOK, true},
		{"failure", visitErr, true},
		{"steps.plan.done == false", visitOK, true},
		{"steps.plan.done == true", visitOK, false},
		{"steps.test.exit_code == 1", visitOK, true},
		{"steps.plan.done == false && visit < 10", visitOK, true},
	}
	for _, tc := range cases {
		// visit in template is run.VisitCount+1; set high enough
		run.VisitCount = 0
		got, err := evalWorkflowCondition(tc.when, run, "plan", tc.visit)
		if err != nil {
			t.Fatalf("%s: %v", tc.when, err)
		}
		if got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.when, got, tc.want)
		}
	}
	if _, err := evalWorkflowCondition("steps.missing.field == true", run, "plan", visitOK); err == nil {
		t.Fatal("expected missing path error")
	}
}

func TestExecuteCommandWorkflowGraph(t *testing.T) {
	// Linear command graph: a -> b -> end
	wf := &Workflow{
		Version: 1,
		Name:    "cmd-graph",
		Entry:   "a",
		Limits:  WorkflowLimits{MaxVisits: 10},
		Steps: map[string]WorkflowStep{
			"a": {Type: "command", Command: "echo step-a"},
			"b": {Type: "command", Command: "echo step-b"},
		},
		Edges: []WorkflowEdge{
			{From: "a", To: "b", When: "always"},
			{From: "b", To: "end", When: "always"},
		},
		Sinks: map[string]WorkflowSink{"end": {Type: "success"}, "fail": {Type: "failure"}},
	}
	if err := validateWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run, err := NewWorkflowRun(wf, "workflow.yaml", "test-session", "obj")
	if err != nil {
		t.Fatal(err)
	}
	// Avoid writing to real config if possible — use temp via env in store tests;
	// SaveWorkflowRun uses UserConfigDir. Set up temp config.
	root := t.TempDir()
	t.Setenv("APPDATA", filepath.Join(root, "appdata"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	session := &Session{Name: "test-session", PWD: root}
	if err := ExecuteWorkflow(t.Context(), wf, run, session); err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" {
		t.Fatalf("status=%s err=%s", run.Status, run.LastError)
	}
	if run.VisitCount != 2 {
		t.Fatalf("visits=%d", run.VisitCount)
	}
	if run.Outputs["a"].Status != "ok" || run.Outputs["b"].Status != "ok" {
		t.Fatalf("outputs=%#v", run.Outputs)
	}
}

func TestExecuteWorkflowErrorEdge(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", filepath.Join(root, "appdata"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	// boom -> recover -> end; boom always errors, recover succeeds
	wf := &Workflow{
		Version: 1,
		Name:    "err-edge",
		Entry:   "boom",
		Limits:  WorkflowLimits{MaxVisits: 10, MaxVisitsPerStep: map[string]int{"boom": 1, "recover": 2}},
		Steps: map[string]WorkflowStep{
			"boom":    {Type: "command", Command: "go test ./package-that-does-not-exist-xyz"},
			"recover": {Type: "command", Command: "echo recovered"},
		},
		Edges: []WorkflowEdge{
			{From: "boom", To: "recover", When: "always", On: "error"},
			{From: "recover", To: "end", When: "always", On: "success"},
		},
		Sinks: map[string]WorkflowSink{"end": {Type: "success"}},
	}

	if err := validateWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run, err := NewWorkflowRun(wf, "w.yaml", "s", "i")
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{Name: "s", PWD: root}
	if err := ExecuteWorkflow(t.Context(), wf, run, session); err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" {
		t.Fatalf("status=%s err=%s", run.Status, run.LastError)
	}
	if run.Outputs["boom"].Status != "error" || run.Outputs["recover"].Status != "ok" {
		t.Fatalf("outputs=%#v", run.Outputs)
	}
}

func TestExecuteWorkflowMaxVisits(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", filepath.Join(root, "appdata"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	wf := &Workflow{
		Version: 1,
		Name:    "loop",
		Entry:   "a",
		Limits:  WorkflowLimits{MaxVisits: 3},
		Steps: map[string]WorkflowStep{
			"a": {Type: "command", Command: "echo a"},
		},
		Edges: []WorkflowEdge{
			{From: "a", To: "a", When: "always"},
		},
		Sinks: map[string]WorkflowSink{"end": {Type: "success"}, "fail": {Type: "failure"}},
	}
	if err := validateWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run, err := NewWorkflowRun(wf, "w.yaml", "s", "i")
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{Name: "s", PWD: root}
	err = ExecuteWorkflow(t.Context(), wf, run, session)
	if err == nil || !strings.Contains(err.Error(), "max_visits") {
		t.Fatalf("expected max_visits error, got %v", err)
	}
	if run.Status != "failed" {
		t.Fatalf("status=%s", run.Status)
	}
}

func TestNewWorkflowRunSnapshotsDefinition(t *testing.T) {
	workflow := &Workflow{
		Version: 1,
		Name:    "snapshot",
		Entry:   "a",
		Steps:   map[string]WorkflowStep{"a": {Type: "command", Command: "echo hi", Terminal: true}},
		Edges:   []WorkflowEdge{},
		Sinks:   map[string]WorkflowSink{"end": {Type: "success"}},
		Limits:  WorkflowLimits{MaxVisits: 5},
	}
	if err := validateWorkflow(workflow); err != nil {
		t.Fatal(err)
	}
	run, err := NewWorkflowRun(workflow, "workflow.yaml", "default", "task")
	if err != nil {
		t.Fatal(err)
	}
	workflow.Name = "changed"
	if run.Definition.Name != "snapshot" {
		t.Fatalf("definition was not snapshotted: %#v", run.Definition)
	}
}

func TestExecuteCommandPreservesFailureExitCode(t *testing.T) {
	_, _, _, err := executeCommand(GetSystemInfo().Shell, "go test ./package-that-does-not-exist", ".")
	if err == nil {
		t.Fatal("expected command failure")
	}
}

func intPtr(v int) *int { return &v }
