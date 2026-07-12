package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

// --- Definition ---

type Workflow struct {
	Version     int                     `yaml:"version" json:"version"`
	Name        string                  `yaml:"name" json:"name"`
	Description string                  `yaml:"description,omitempty" json:"description,omitempty"`
	Entry       string                  `yaml:"entry" json:"entry"`
	Limits      WorkflowLimits          `yaml:"limits,omitempty" json:"limits,omitempty"`
	Steps       map[string]WorkflowStep `yaml:"steps" json:"steps"`
	Edges       []WorkflowEdge          `yaml:"edges" json:"edges"`
	Sinks       map[string]WorkflowSink `yaml:"sinks,omitempty" json:"sinks,omitempty"`
	Defaults    WorkflowDefaults        `yaml:"defaults,omitempty" json:"defaults,omitempty"`

	// Loop is only used to detect and reject legacy fixed-loop documents.
	Loop any `yaml:"loop,omitempty" json:"-"`
}

type WorkflowLimits struct {
	MaxVisits         int            `yaml:"max_visits,omitempty" json:"max_visits,omitempty"`
	MaxVisitsPerStep  map[string]int `yaml:"max_visits_per_step,omitempty" json:"max_visits_per_step,omitempty"`
	MaxDuration       string         `yaml:"max_duration,omitempty" json:"max_duration,omitempty"`
	maxDurationParsed time.Duration  `yaml:"-" json:"-"`
}

type WorkflowDefaults struct {
	RepairAttempts int `yaml:"repair_attempts,omitempty" json:"repair_attempts,omitempty"`
}

type WorkflowStep struct {
	Type           string              `yaml:"type" json:"type"`
	Description    string              `yaml:"description,omitempty" json:"description,omitempty"`
	Terminal       bool                `yaml:"terminal,omitempty" json:"terminal,omitempty"`
	Provider       string              `yaml:"provider,omitempty" json:"provider,omitempty"`
	Prompt         string              `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	Command        string              `yaml:"command,omitempty" json:"command,omitempty"`
	Output         *WorkflowStepOutput `yaml:"output,omitempty" json:"output,omitempty"`
	RepairAttempts *int                `yaml:"repair_attempts,omitempty" json:"repair_attempts,omitempty"`
}

type WorkflowStepOutput struct {
	Kind       string         `yaml:"kind,omitempty" json:"kind,omitempty"`
	Schema     string         `yaml:"schema,omitempty" json:"schema,omitempty"`
	JSONSchema map[string]any `yaml:"json_schema,omitempty" json:"json_schema,omitempty"`
}

type WorkflowEdge struct {
	From        string `yaml:"from" json:"from"`
	To          string `yaml:"to" json:"to"`
	When        string `yaml:"when" json:"when"`
	On          string `yaml:"on,omitempty" json:"on,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

type WorkflowSink struct {
	Type    string `yaml:"type" json:"type"`
	Message string `yaml:"message,omitempty" json:"message,omitempty"`
}

// --- Run state ---

type WorkflowRun struct {
	ID            string                `json:"id"`
	WorkflowName  string                `json:"workflow_name"`
	WorkflowPath  string                `json:"workflow_path"`
	Definition    Workflow              `json:"definition"`
	SessionName   string                `json:"session_name"`
	WorkingDir    string                `json:"working_dir"`
	Input         string                `json:"input"`
	Status        string                `json:"status"`
	Cursor        string                `json:"cursor"`
	VisitCount    int                   `json:"visit_count"`
	VisitsPerStep map[string]int        `json:"visits_per_step"`
	Outputs       map[string]StepOutput `json:"outputs"`
	Visits        []WorkflowVisit       `json:"visits"`
	LastError     string                `json:"last_error,omitempty"`
	LogPath       string                `json:"log_path,omitempty"`
	CreatedAt     time.Time             `json:"created_at"`
	UpdatedAt     time.Time             `json:"updated_at"`
	StartedAt     time.Time             `json:"started_at,omitempty"`
}

type StepOutput struct {
	Raw      string         `json:"raw"`
	Parsed   map[string]any `json:"parsed,omitempty"`
	ExitCode *int           `json:"exit_code,omitempty"`
	Status   string         `json:"status"` // ok | error
}

type WorkflowVisit struct {
	Index      int            `json:"index"`
	StepID     string         `json:"step_id"`
	Status     string         `json:"status"` // ok | error
	Raw        string         `json:"raw,omitempty"`
	Parsed     map[string]any `json:"parsed,omitempty"`
	ExitCode   *int           `json:"exit_code,omitempty"`
	Error      string         `json:"error,omitempty"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt time.Time      `json:"finished_at"`
}

// PlanOutput is the built-in "plan" structured schema.
type PlanOutput struct {
	Done    bool         `json:"done"`
	Summary string       `json:"summary"`
	Issues  []PlanIssue  `json:"issues"`
	Plan    []PlanAction `json:"plan"`
}

type PlanIssue struct {
	ID                 string `json:"id"`
	Severity           string `json:"severity"`
	Description        string `json:"description"`
	Evidence           string `json:"evidence"`
	AcceptanceCriteria string `json:"acceptance_criteria"`
}

type PlanAction struct {
	IssueID string `json:"issue_id"`
	Action  string `json:"action"`
}

// VerdictOutput is the built-in "verdict" structured schema.
type VerdictOutput struct {
	Pass     bool     `json:"pass"`
	Summary  string   `json:"summary"`
	Gaps     []string `json:"gaps,omitempty"`
	Evidence string   `json:"evidence,omitempty"`
}

var (
	stepIDPattern        = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	templatePlaceholder  = regexp.MustCompile(`\{\{\s*([^}]+?)\s*\}\}`)
	errLegacyWorkflow    = errors.New("unsupported workflow format: expected graph document (version 1 with steps/edges); legacy loop.review is not supported — see docs/workflow-dag-schema.md")
	errLegacyWorkflowRun = errors.New("legacy workflow run is not supported")
)

var reservedStepIDs = map[string]bool{
	"end": true, "success": true, "fail": true,
	"input": true, "visit": true, "step": true, "steps": true, "history": true,
}

// --- Load / validate ---

func LoadWorkflow(path string) (*Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseWorkflow(data)
}

func parseWorkflow(data []byte) (*Workflow, error) {
	var workflow Workflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		return nil, err
	}
	if err := validateWorkflow(&workflow); err != nil {
		return nil, err
	}
	return &workflow, nil
}

func validateWorkflow(workflow *Workflow) error {
	if workflow.Loop != nil {
		return errLegacyWorkflow
	}
	if workflow.Version != 1 {
		return fmt.Errorf("unsupported workflow version %d", workflow.Version)
	}
	if strings.TrimSpace(workflow.Name) == "" {
		return errors.New("workflow name is required")
	}
	if len(workflow.Steps) == 0 {
		return errLegacyWorkflow
	}
	if workflow.Edges == nil {
		workflow.Edges = []WorkflowEdge{}
	}
	if strings.TrimSpace(workflow.Entry) == "" {
		return errors.New("entry is required")
	}
	if _, ok := workflow.Steps[workflow.Entry]; !ok {
		return fmt.Errorf("entry %q is not a defined step", workflow.Entry)
	}

	for id, step := range workflow.Steps {
		if !stepIDPattern.MatchString(id) || reservedStepIDs[id] {
			return fmt.Errorf("invalid step id %q", id)
		}
		step.Type = strings.ToLower(strings.TrimSpace(step.Type))
		step.Provider = strings.ToLower(strings.TrimSpace(step.Provider))
		normalized, err := normalizeWorkflowStep(id, step)
		if err != nil {
			return err
		}
		workflow.Steps[id] = normalized
	}

	if workflow.Sinks == nil {
		workflow.Sinks = map[string]WorkflowSink{}
	}
	for id, sink := range workflow.Sinks {
		if !stepIDPattern.MatchString(id) {
			return fmt.Errorf("invalid sink id %q", id)
		}
		sink.Type = strings.ToLower(strings.TrimSpace(sink.Type))
		if sink.Type != "success" && sink.Type != "failure" {
			return fmt.Errorf("sink %q type must be success or failure", id)
		}
		workflow.Sinks[id] = sink
	}
	// Built-in sinks when not explicitly defined.
	if _, ok := workflow.Sinks["end"]; !ok {
		workflow.Sinks["end"] = WorkflowSink{Type: "success"}
	}
	if _, ok := workflow.Sinks["fail"]; !ok {
		workflow.Sinks["fail"] = WorkflowSink{Type: "failure"}
	}

	for i := range workflow.Edges {
		e := &workflow.Edges[i]
		e.From = strings.TrimSpace(e.From)
		e.To = strings.TrimSpace(e.To)
		e.When = strings.TrimSpace(e.When)
		e.On = strings.ToLower(strings.TrimSpace(e.On))
		if e.On == "" {
			e.On = "success"
		}
		if e.On != "success" && e.On != "error" && e.On != "always" {
			return fmt.Errorf("edge %d: on must be success, error, or always", i)
		}
		if _, ok := workflow.Steps[e.From]; !ok {
			return fmt.Errorf("edge %d: unknown from step %q", i, e.From)
		}
		if _, isStep := workflow.Steps[e.To]; !isStep {
			if _, isSink := workflow.Sinks[e.To]; !isSink {
				return fmt.Errorf("edge %d: unknown to target %q", i, e.To)
			}
		}
		if e.When == "" {
			return fmt.Errorf("edge %d: when is required", i)
		}
	}

	if workflow.Limits.MaxVisits == 0 {
		workflow.Limits.MaxVisits = 32
	}
	if workflow.Limits.MaxVisits < 0 {
		return errors.New("limits.max_visits must be positive")
	}
	for id, n := range workflow.Limits.MaxVisitsPerStep {
		if _, ok := workflow.Steps[id]; !ok {
			return fmt.Errorf("limits.max_visits_per_step unknown step %q", id)
		}
		if n <= 0 {
			return fmt.Errorf("limits.max_visits_per_step.%s must be positive", id)
		}
	}
	if d := strings.TrimSpace(workflow.Limits.MaxDuration); d != "" {
		parsed, err := time.ParseDuration(d)
		if err != nil {
			return fmt.Errorf("limits.max_duration: %w", err)
		}
		if parsed <= 0 {
			return errors.New("limits.max_duration must be positive")
		}
		workflow.Limits.maxDurationParsed = parsed
	}
	if workflow.Defaults.RepairAttempts < 0 {
		return errors.New("defaults.repair_attempts cannot be negative")
	}
	return nil
}

func normalizeWorkflowStep(id string, step WorkflowStep) (WorkflowStep, error) {
	switch step.Type {
	case "provider":
		if step.Provider != "codex" && step.Provider != "grok" && step.Provider != "claude" && step.Provider != "agy" {
			return step, fmt.Errorf("step %q: provider must be codex, grok, claude, or agy", id)
		}
		if strings.TrimSpace(step.Prompt) == "" {
			return step, fmt.Errorf("step %q: prompt is required", id)
		}
		if step.Output != nil {
			kind := strings.ToLower(strings.TrimSpace(step.Output.Kind))
			if kind == "" {
				kind = "text"
			}
			step.Output.Kind = kind
			if kind != "text" && kind != "structured" {
				return step, fmt.Errorf("step %q: output.kind must be text or structured", id)
			}
			if kind == "structured" {
				schema := strings.ToLower(strings.TrimSpace(step.Output.Schema))
				step.Output.Schema = schema
				hasSchema := schema != ""
				hasJSON := len(step.Output.JSONSchema) > 0
				if !hasSchema && !hasJSON {
					return step, fmt.Errorf("step %q: structured output requires schema or json_schema", id)
				}
				if hasSchema && hasJSON {
					return step, fmt.Errorf("step %q: set only one of schema or json_schema", id)
				}
				if hasSchema && schema != "plan" && schema != "verdict" {
					return step, fmt.Errorf("step %q: unknown built-in schema %q", id, schema)
				}
			}
		}
	case "command":
		if strings.TrimSpace(step.Command) == "" {
			return step, fmt.Errorf("step %q: command is required", id)
		}
	default:
		return step, fmt.Errorf("step %q: type must be provider or command", id)
	}
	return step, nil
}

// --- Run lifecycle ---

func NewWorkflowRun(workflow *Workflow, path, sessionName, input string) (*WorkflowRun, error) {
	id, err := newUUID()
	if err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	logPath := filepath.Join(workingDir, ".q", "workflow-logs", id+".log")
	return &WorkflowRun{
		ID:            id,
		WorkflowName:  workflow.Name,
		WorkflowPath:  absPath,
		Definition:    *workflow,
		SessionName:   sessionName,
		WorkingDir:    workingDir,
		Input:         input,
		Status:        "pending",
		Cursor:        workflow.Entry,
		VisitsPerStep: map[string]int{},
		Outputs:       map[string]StepOutput{},
		Visits:        []WorkflowVisit{},
		LogPath:       logPath,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func ExecuteWorkflow(ctx context.Context, workflow *Workflow, run *WorkflowRun, session *Session) error {
	if len(workflow.Steps) == 0 {
		return failWorkflowRun(run, errLegacyWorkflowRun)
	}
	if err := validateWorkflow(workflow); err != nil {
		return failWorkflowRun(run, err)
	}
	run.Definition = *workflow
	if run.Outputs == nil {
		run.Outputs = map[string]StepOutput{}
	}
	if run.VisitsPerStep == nil {
		run.VisitsPerStep = map[string]int{}
	}
	if run.Cursor == "" {
		run.Cursor = workflow.Entry
	}
	run.Status, run.LastError = "running", ""
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	if strings.TrimSpace(run.LogPath) == "" {
		base := run.WorkingDir
		if base == "" {
			base, _ = os.Getwd()
		}
		run.LogPath = filepath.Join(base, ".q", "workflow-logs", run.ID+".log")
	}
	if err := writeWorkflowLogHeader(run); err != nil {
		PrintWarning(fmt.Sprintf("workflow log header: %v", err))
	} else {
		PrintInfo(fmt.Sprintf("Workflow log: %s", run.LogPath))
	}
	if err := SaveWorkflowRun(run); err != nil {
		return err
	}

	for {
		if err := checkWorkflowLimits(workflow, run); err != nil {
			_ = appendWorkflowLog(run, fmt.Sprintf("LIMIT/ERROR: %v\n", err))
			return failWorkflowRun(run, err)
		}

		if sink, ok := workflow.Sinks[run.Cursor]; ok {
			if _, isStep := workflow.Steps[run.Cursor]; !isStep {
				return finishWorkflowSink(run, sink)
			}
		}

		step, ok := workflow.Steps[run.Cursor]
		if !ok {
			return failWorkflowRun(run, fmt.Errorf("cursor %q is not a step or sink", run.Cursor))
		}

		PrintInfo(fmt.Sprintf("Workflow %s: visit %d step %q (%s)", run.ID, run.VisitCount+1, run.Cursor, step.Type))
		if err := SaveWorkflowRun(run); err != nil {
			return err
		}

		visit := executeWorkflowStep(ctx, session, workflow, run, run.Cursor, step)
		run.VisitCount++
		run.VisitsPerStep[run.Cursor]++
		run.Visits = append(run.Visits, visit)

		out := StepOutput{Raw: visit.Raw, Parsed: visit.Parsed, ExitCode: visit.ExitCode, Status: visit.Status}
		run.Outputs[run.Cursor] = out

		logVisitToConsole(visit)
		if err := appendWorkflowVisitLog(run, visit); err != nil {
			PrintWarning(fmt.Sprintf("workflow log append: %v", err))
		}

		if err := SaveWorkflowRun(run); err != nil {
			return err
		}

		next, term, err := pickWorkflowNext(workflow, run, run.Cursor, visit)
		if err != nil {
			_ = appendWorkflowLog(run, fmt.Sprintf("EDGE ERROR after %q: %v\n", run.Cursor, err))
			return failWorkflowRun(run, err)
		}
		if term != nil {
			_ = appendWorkflowLog(run, fmt.Sprintf("NEXT: sink type=%s message=%q\n", term.Type, term.Message))
			return finishWorkflowSink(run, *term)
		}
		_ = appendWorkflowLog(run, fmt.Sprintf("NEXT: %s -> %s\n", run.Cursor, next))
		run.Cursor = next
		if err := SaveWorkflowRun(run); err != nil {
			return err
		}
	}
}

func checkWorkflowLimits(workflow *Workflow, run *WorkflowRun) error {
	if workflow.Limits.MaxVisits > 0 && run.VisitCount >= workflow.Limits.MaxVisits {
		return fmt.Errorf("workflow reached max_visits=%d", workflow.Limits.MaxVisits)
	}
	if d := workflow.Limits.maxDurationParsed; d > 0 && !run.StartedAt.IsZero() {
		if time.Since(run.StartedAt) > d {
			return fmt.Errorf("workflow exceeded max_duration=%s", workflow.Limits.MaxDuration)
		}
	}
	// Per-step check happens after we know cursor; also pre-check if next visit would exceed.
	if cap, ok := workflow.Limits.MaxVisitsPerStep[run.Cursor]; ok {
		if run.VisitsPerStep[run.Cursor] >= cap {
			return fmt.Errorf("workflow reached max_visits_per_step.%s=%d", run.Cursor, cap)
		}
	}
	return nil
}

func finishWorkflowSink(run *WorkflowRun, sink WorkflowSink) error {
	switch sink.Type {
	case "success":
		run.Status = "completed"
		run.LastError = ""
		if sink.Message != "" {
			PrintInfo(sink.Message)
		}
		_ = appendWorkflowLog(run, fmt.Sprintf("\n=== COMPLETED ===\nstatus=completed visits=%d\nlog=%s\n", run.VisitCount, run.LogPath))
		if run.LogPath != "" {
			PrintSuccess(fmt.Sprintf("Workflow log written: %s", run.LogPath))
		}
		return SaveWorkflowRun(run)
	default:
		msg := sink.Message
		if msg == "" {
			msg = "workflow ended on failure sink"
		}
		return failWorkflowRun(run, errors.New(msg))
	}
}

func failWorkflowRun(run *WorkflowRun, err error) error {
	run.Status, run.LastError = "failed", err.Error()
	_ = appendWorkflowLog(run, fmt.Sprintf("\n=== FAILED ===\nstatus=failed error=%s visits=%d\nlog=%s\n", err.Error(), run.VisitCount, run.LogPath))
	if run.LogPath != "" {
		PrintWarning(fmt.Sprintf("Workflow log written: %s", run.LogPath))
	}
	_ = SaveWorkflowRun(run)
	return err
}

const workflowLogConsoleMax = 1200

func workflowLogDir(run *WorkflowRun) string {
	base := run.WorkingDir
	if base == "" {
		base, _ = os.Getwd()
	}
	return filepath.Join(base, ".q", "workflow-logs")
}

func ensureWorkflowLogPath(run *WorkflowRun) string {
	if strings.TrimSpace(run.LogPath) == "" {
		run.LogPath = filepath.Join(workflowLogDir(run), run.ID+".log")
	}
	return run.LogPath
}

func writeWorkflowLogHeader(run *WorkflowRun) error {
	path := ensureWorkflowLogPath(run)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	// Truncate on first start; append on resume if file already has content.
	flag := os.O_CREATE | os.O_WRONLY
	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flag, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "=== workflow run %s ===\nname=%s\ninput=%s\nworking_dir=%s\nsession=%s\nstarted=%s\n\n",
		run.ID, run.WorkflowName, run.Input, run.WorkingDir, run.SessionName, time.Now().Format(time.RFC3339))
	return err
}

func appendWorkflowLog(run *WorkflowRun, text string) error {
	path := ensureWorkflowLogPath(run)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(text)
	return err
}

func appendWorkflowVisitLog(run *WorkflowRun, visit WorkflowVisit) error {
	var b strings.Builder
	fmt.Fprintf(&b, "--- visit %d step=%q status=%s ---\n", visit.Index, visit.StepID, visit.Status)
	fmt.Fprintf(&b, "started=%s finished=%s\n", visit.StartedAt.Format(time.RFC3339), visit.FinishedAt.Format(time.RFC3339))
	if visit.ExitCode != nil {
		fmt.Fprintf(&b, "exit_code=%d\n", *visit.ExitCode)
	}
	if visit.Error != "" {
		fmt.Fprintf(&b, "error=%s\n", visit.Error)
	}
	if visit.Parsed != nil {
		if raw, err := json.MarshalIndent(visit.Parsed, "", "  "); err == nil {
			fmt.Fprintf(&b, "parsed:\n%s\n", raw)
		}
	}
	fmt.Fprintf(&b, "raw:\n%s\n\n", visit.Raw)
	return appendWorkflowLog(run, b.String())
}

func logVisitToConsole(visit WorkflowVisit) {
	if visit.Status == "error" {
		PrintWarning(fmt.Sprintf("step %q failed: %s", visit.StepID, visit.Error))
	} else {
		PrintInfo(fmt.Sprintf("step %q ok (output %d bytes)", visit.StepID, len(visit.Raw)))
	}
	preview := strings.TrimSpace(visit.Raw)
	if preview == "" {
		PrintWarning(fmt.Sprintf("step %q produced empty output", visit.StepID))
		return
	}
	if len(preview) > workflowLogConsoleMax {
		preview = preview[:workflowLogConsoleMax] + "\n… (truncated; see workflow log file)"
	}
	fmt.Println(Dim("──── step output ────"))
	fmt.Println(preview)
	fmt.Println(Dim("─────────────────────"))
}

func pickWorkflowNext(workflow *Workflow, run *WorkflowRun, from string, visit WorkflowVisit) (next string, sink *WorkflowSink, err error) {
	step := workflow.Steps[from]
	visitOK := visit.Status == "ok"

	for _, edge := range workflow.Edges {
		if edge.From != from {
			continue
		}
		on := edge.On
		if on == "" {
			on = "success"
		}
		switch on {
		case "success":
			if !visitOK {
				continue
			}
		case "error":
			if visitOK {
				continue
			}
		case "always":
			// ok
		}
		ok, evalErr := evalWorkflowCondition(edge.When, run, from, visit)
		if evalErr != nil {
			return "", nil, fmt.Errorf("edge %s -> %s when: %w", edge.From, edge.To, evalErr)
		}
		if !ok {
			continue
		}
		if s, isSink := workflow.Sinks[edge.To]; isSink {
			if _, isStep := workflow.Steps[edge.To]; !isStep {
				return "", &s, nil
			}
		}
		return edge.To, nil, nil
	}

	if step.Terminal {
		s := WorkflowSink{Type: "success"}
		return "", &s, nil
	}
	if !visitOK {
		return "", nil, fmt.Errorf("step %q failed and no matching error edge", from)
	}
	return "", nil, fmt.Errorf("no matching edge from step %q", from)
}

// --- Step execution ---

func executeWorkflowStep(ctx context.Context, session *Session, workflow *Workflow, run *WorkflowRun, stepID string, step WorkflowStep) WorkflowVisit {
	started := time.Now()
	visit := WorkflowVisit{Index: run.VisitCount + 1, StepID: stepID, StartedAt: started}

	switch step.Type {
	case "command":
		cmd := renderWorkflowTemplate(step.Command, buildTemplateContext(run, stepID))
		stdout, stderr, finalDir, err := executeCommand(GetSystemInfo().Shell, cmd, session.PWD)
		visit.Raw = string(stdout) + string(stderr)
		if finalDir != "" {
			session.PWD = finalDir
			_ = SaveSession(session)
		}
		if err != nil {
			code := 1
			visit.ExitCode = &code
			visit.Status = "error"
			visit.Error = err.Error()
		} else {
			code := 0
			visit.ExitCode = &code
			visit.Status = "ok"
		}
	case "provider":
		prompt := renderWorkflowTemplate(step.Prompt, buildTemplateContext(run, stepID))
		repairs := workflow.Defaults.RepairAttempts
		if repairs == 0 {
			repairs = 2
		}
		if step.RepairAttempts != nil {
			repairs = *step.RepairAttempts
		}
		kind := "text"
		if step.Output != nil && step.Output.Kind != "" {
			kind = step.Output.Kind
		}
		if kind == "structured" {
			raw, parsed, err := callStructuredStep(ctx, session, step, prompt, repairs)
			visit.Raw = raw
			visit.Parsed = parsed
			if err != nil {
				visit.Status = "error"
				visit.Error = err.Error()
			} else {
				visit.Status = "ok"
			}
		} else {
			raw, err := callWorkflowProvider(ctx, session, step.Provider, prompt, "")
			visit.Raw = raw
			if err != nil {
				visit.Status = "error"
				visit.Error = err.Error()
			} else {
				visit.Status = "ok"
			}
		}
	default:
		visit.Status = "error"
		visit.Error = fmt.Sprintf("unknown step type %q", step.Type)
	}

	visit.FinishedAt = time.Now()
	return visit
}

func callStructuredStep(ctx context.Context, session *Session, step WorkflowStep, prompt string, repairs int) (string, map[string]any, error) {
	schemaValue, err := structuredSchemaForStep(step)
	if err != nil {
		return "", nil, err
	}
	schemaJSON, _ := json.Marshal(schemaValue)
	currentPrompt := prompt
	var raw string
	for attempt := 0; attempt <= repairs; attempt++ {
		raw, err = callWorkflowProvider(ctx, session, step.Provider, currentPrompt, string(schemaJSON))
		if err != nil {
			return raw, nil, err
		}
		payload, perr := extractStructuredPayload(step.Provider, raw)
		if perr == nil {
			var asMap map[string]any
			if uerr := json.Unmarshal(payload, &asMap); uerr == nil {
				if verr := validateStructuredPayload(step, payload); verr == nil {
					return string(payload), asMap, nil
				} else {
					perr = verr
				}
			} else {
				perr = uerr
			}
		}
		if attempt == repairs {
			return raw, nil, fmt.Errorf("invalid structured output from %s: %w", step.Provider, perr)
		}
		currentPrompt = "Your previous response failed structured-output validation. Do not use tools. Return only a corrected JSON object matching the requested schema. Validation error: " + perr.Error()
	}
	return raw, nil, errors.New("unreachable structured step state")
}

func structuredSchemaForStep(step WorkflowStep) (map[string]any, error) {
	if step.Output == nil {
		return nil, errors.New("structured output missing")
	}
	if len(step.Output.JSONSchema) > 0 {
		return step.Output.JSONSchema, nil
	}
	switch step.Output.Schema {
	case "plan":
		return planOutputSchema(), nil
	case "verdict":
		return verdictOutputSchema(), nil
	default:
		return nil, fmt.Errorf("unknown schema %q", step.Output.Schema)
	}
}

func validateStructuredPayload(step WorkflowStep, payload []byte) error {
	if step.Output != nil && len(step.Output.JSONSchema) > 0 {
		// Inline schema: only require valid JSON object.
		var obj map[string]any
		if err := json.Unmarshal(payload, &obj); err != nil {
			return err
		}
		return nil
	}
	schema := ""
	if step.Output != nil {
		schema = step.Output.Schema
	}
	switch schema {
	case "plan":
		var plan PlanOutput
		if err := json.Unmarshal(payload, &plan); err != nil {
			return err
		}
		return validatePlanOutput(&plan)
	case "verdict":
		var v VerdictOutput
		if err := json.Unmarshal(payload, &v); err != nil {
			return err
		}
		return validateVerdictOutput(&v)
	default:
		return fmt.Errorf("unknown schema %q", schema)
	}
}

func validatePlanOutput(plan *PlanOutput) error {
	if strings.TrimSpace(plan.Summary) == "" {
		return errors.New("summary is required")
	}
	if plan.Done && len(plan.Issues) > 0 {
		return errors.New("done=true cannot include issues")
	}
	if !plan.Done && len(plan.Issues) == 0 {
		return errors.New("done=false must include at least one issue")
	}
	ids := make(map[string]bool)
	for _, issue := range plan.Issues {
		if issue.ID == "" || issue.Description == "" || issue.Evidence == "" || issue.AcceptanceCriteria == "" {
			return errors.New("issue fields cannot be empty")
		}
		if issue.Severity != "low" && issue.Severity != "medium" && issue.Severity != "high" {
			return fmt.Errorf("invalid severity %q", issue.Severity)
		}
		ids[issue.ID] = true
	}
	planned := make(map[string]bool)
	for _, p := range plan.Plan {
		if !ids[p.IssueID] || p.Action == "" {
			return errors.New("plan must reference an issue and include an action")
		}
		planned[p.IssueID] = true
	}
	for id := range ids {
		if !planned[id] {
			return fmt.Errorf("issue %q has no improvement plan", id)
		}
	}
	return nil
}

func validateVerdictOutput(v *VerdictOutput) error {
	if strings.TrimSpace(v.Summary) == "" {
		return errors.New("summary is required")
	}
	if v.Pass && len(v.Gaps) > 0 {
		return errors.New("pass=true cannot include gaps")
	}
	if !v.Pass && len(v.Gaps) == 0 {
		return errors.New("pass=false must include at least one gap")
	}
	return nil
}

func planOutputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"required":             []string{"done", "summary", "issues", "plan"},
		"additionalProperties": false,
		"properties": map[string]any{
			"done":    map[string]any{"type": "boolean"},
			"summary": map[string]any{"type": "string", "minLength": 1},
			"issues": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "required": []string{"id", "severity", "description", "evidence", "acceptance_criteria"},
				"additionalProperties": false,
				"properties": map[string]any{
					"id": map[string]any{"type": "string"}, "severity": map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
					"description": map[string]any{"type": "string"}, "evidence": map[string]any{"type": "string"},
					"acceptance_criteria": map[string]any{"type": "string"},
				},
			}},
			"plan": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "required": []string{"issue_id", "action"}, "additionalProperties": false,
				"properties": map[string]any{"issue_id": map[string]any{"type": "string"}, "action": map[string]any{"type": "string"}},
			}},
		},
	}
}

func verdictOutputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"required":             []string{"pass", "summary"},
		"additionalProperties": false,
		"properties": map[string]any{
			"pass":     map[string]any{"type": "boolean"},
			"summary":  map[string]any{"type": "string", "minLength": 1},
			"gaps":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"evidence": map[string]any{"type": "string"},
		},
	}
}

func callWorkflowProvider(ctx context.Context, session *Session, provider, prompt, schema string) (string, error) {
	switch strings.ToLower(provider) {
	case "codex":
		var value any
		if schema != "" && json.Unmarshal([]byte(schema), &value) != nil {
			return "", errors.New("invalid Codex output schema")
		}
		return callCodexForSession(ctx, session, prompt, value)
	case "grok":
		return callGrokForSession(ctx, session, prompt, schema)
	case "claude":
		return callClaudeForSession(ctx, session, prompt, schema)
	case "agy":
		if schema != "" {
			prompt += "\nReturn ONLY one JSON object without markdown fences matching this JSON Schema:\n" + schema
		}
		return callAgyForSession(ctx, session, prompt)
	default:
		return "", fmt.Errorf("unsupported provider %q", provider)
	}
}

func extractStructuredPayload(provider, raw string) ([]byte, error) {
	provider = strings.ToLower(provider)
	raw = strings.TrimSpace(raw)
	if provider == "codex" || provider == "agy" {
		if !json.Valid([]byte(raw)) {
			return nil, errors.New("response is not a single JSON value")
		}
		return []byte(raw), nil
	}
	// Grok/Claude headless JSON wraps the schema object. Prefer the dedicated
	// field; if the model only put JSON in "text" (structuredOutput null +
	// structuredOutputError), fall back so workflow steps still work.
	var wrapper struct {
		StructuredOutput      json.RawMessage `json:"structuredOutput"`
		StructuredOutputSnake json.RawMessage `json:"structured_output"`
		Text                  string          `json:"text"`
		StructuredOutputError string          `json:"structuredOutputError"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		// Bare JSON object (no wrapper) — accept if valid.
		if json.Valid([]byte(raw)) {
			return []byte(raw), nil
		}
		return nil, err
	}
	payload := wrapper.StructuredOutput
	if provider == "claude" {
		if len(wrapper.StructuredOutputSnake) > 0 && string(wrapper.StructuredOutputSnake) != "null" {
			payload = wrapper.StructuredOutputSnake
		}
	}
	if len(payload) > 0 && string(payload) != "null" {
		return payload, nil
	}
	// Fallback: text field may hold a JSON object string.
	if text := strings.TrimSpace(wrapper.Text); text != "" {
		if json.Valid([]byte(text)) {
			return []byte(text), nil
		}
		// Sometimes text is a fenced or quoted blob; try unquote once.
		var unquoted string
		if err := json.Unmarshal([]byte(text), &unquoted); err == nil {
			unquoted = strings.TrimSpace(unquoted)
			if json.Valid([]byte(unquoted)) {
				return []byte(unquoted), nil
			}
		}
	}
	if wrapper.StructuredOutputError != "" {
		return nil, fmt.Errorf("structured output field is missing (%s)", wrapper.StructuredOutputError)
	}
	return nil, errors.New("structured output field is missing")
}

// --- Templates ---

func buildTemplateContext(run *WorkflowRun, stepID string) map[string]any {
	steps := map[string]any{}
	for id, out := range run.Outputs {
		steps[id] = stepOutputToTemplateValue(out)
	}
	return map[string]any{
		"input": run.Input,
		"visit": float64(run.VisitCount + 1),
		"step":  stepID,
		"steps": steps,
	}
}

func stepOutputToTemplateValue(out StepOutput) map[string]any {
	m := map[string]any{"raw": out.Raw, "status": out.Status}
	if out.ExitCode != nil {
		m["exit_code"] = float64(*out.ExitCode)
	}
	if out.Parsed != nil {
		for k, v := range out.Parsed {
			m[k] = v
		}
	}
	return m
}

func renderWorkflowTemplate(template string, ctx map[string]any) string {
	return templatePlaceholder.ReplaceAllStringFunc(template, func(match string) string {
		inner := templatePlaceholder.FindStringSubmatch(match)
		if len(inner) < 2 {
			return ""
		}
		path := strings.TrimSpace(inner[1])
		val, ok := lookupTemplatePath(ctx, path)
		if !ok {
			return ""
		}
		return templateValueToString(val)
	})
}

func lookupTemplatePath(root map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	var cur any = root
	for _, p := range parts {
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[p]
			if !ok {
				return nil, false
			}
			cur = v
		default:
			return nil, false
		}
	}
	return cur, true
}

func templateValueToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case map[string]any:
		// Step bag: prefer business fields, else raw.
		if raw, ok := t["raw"].(string); ok {
			business := map[string]any{}
			for k, val := range t {
				if k == "raw" || k == "status" || k == "exit_code" {
					continue
				}
				business[k] = val
			}
			if len(business) > 0 {
				b, err := json.Marshal(business)
				if err == nil {
					return string(b)
				}
			}
			return raw
		}
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	}
}

// --- Conditions ---

func evalWorkflowCondition(when string, run *WorkflowRun, stepID string, visit WorkflowVisit) (bool, error) {
	when = strings.TrimSpace(when)
	switch strings.ToLower(when) {
	case "always":
		return true, nil
	case "never":
		return false, nil
	case "success":
		return visit.Status == "ok", nil
	case "failure":
		return visit.Status != "ok", nil
	}

	ctx := buildTemplateContext(run, stepID)
	if visit.Index > 0 {
		ctx["visit"] = float64(visit.Index)
	}
	return evalConditionExpr(when, ctx)
}

// Minimal expression evaluator: || && == != < <= > >= ! ( ) true false numbers strings paths
func evalConditionExpr(expr string, ctx map[string]any) (bool, error) {
	p := &condParser{input: expr, ctx: ctx}
	p.skipSpace()
	v, err := p.parseOr()
	if err != nil {
		return false, err
	}
	p.skipSpace()
	if p.pos < len(p.input) {
		return false, fmt.Errorf("unexpected trailing input at %d", p.pos)
	}
	return truthy(v), nil
}

type condParser struct {
	input string
	pos   int
	ctx   map[string]any
}

func (p *condParser) skipSpace() {
	for p.pos < len(p.input) && unicode.IsSpace(rune(p.input[p.pos])) {
		p.pos++
	}
}

func (p *condParser) parseOr() (any, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		p.skipSpace()
		if !strings.HasPrefix(p.input[p.pos:], "||") {
			return left, nil
		}
		p.pos += 2
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = truthy(left) || truthy(right)
	}
}

func (p *condParser) parseAnd() (any, error) {
	left, err := p.parseCmp()
	if err != nil {
		return nil, err
	}
	for {
		p.skipSpace()
		if !strings.HasPrefix(p.input[p.pos:], "&&") {
			return left, nil
		}
		p.pos += 2
		right, err := p.parseCmp()
		if err != nil {
			return nil, err
		}
		left = truthy(left) && truthy(right)
	}
}

func (p *condParser) parseCmp() (any, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	ops := []string{"==", "!=", "<=", ">=", "<", ">"}
	var op string
	for _, candidate := range ops {
		if strings.HasPrefix(p.input[p.pos:], candidate) {
			op = candidate
			p.pos += len(candidate)
			break
		}
	}
	if op == "" {
		return left, nil
	}
	right, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	return compareValues(left, right, op)
}

func (p *condParser) parseUnary() (any, error) {
	p.skipSpace()
	if p.pos < len(p.input) && p.input[p.pos] == '!' {
		p.pos++
		v, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return !truthy(v), nil
	}
	return p.parsePrimary()
}

func (p *condParser) parsePrimary() (any, error) {
	p.skipSpace()
	if p.pos >= len(p.input) {
		return nil, errors.New("unexpected end of expression")
	}
	if p.input[p.pos] == '(' {
		p.pos++
		v, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return nil, errors.New("missing )")
		}
		p.pos++
		return v, nil
	}
	if p.input[p.pos] == '"' || p.input[p.pos] == '\'' {
		return p.parseString()
	}
	// number
	if unicode.IsDigit(rune(p.input[p.pos])) || (p.input[p.pos] == '-' && p.pos+1 < len(p.input) && unicode.IsDigit(rune(p.input[p.pos+1]))) {
		start := p.pos
		p.pos++
		for p.pos < len(p.input) && (unicode.IsDigit(rune(p.input[p.pos])) || p.input[p.pos] == '.') {
			p.pos++
		}
		n, err := strconv.ParseFloat(p.input[start:p.pos], 64)
		if err != nil {
			return nil, err
		}
		return n, nil
	}
	// identifier / path / true / false
	start := p.pos
	for p.pos < len(p.input) {
		r := rune(p.input[p.pos])
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '-' {
			p.pos++
			continue
		}
		break
	}
	if start == p.pos {
		return nil, fmt.Errorf("unexpected character %q", p.input[p.pos])
	}
	ident := p.input[start:p.pos]
	switch ident {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	val, ok := lookupTemplatePath(p.ctx, ident)
	if !ok {
		return nil, fmt.Errorf("unknown path %q", ident)
	}
	return val, nil
}

func (p *condParser) parseString() (any, error) {
	quote := p.input[p.pos]
	p.pos++
	start := p.pos
	for p.pos < len(p.input) && p.input[p.pos] != quote {
		p.pos++
	}
	if p.pos >= len(p.input) {
		return nil, errors.New("unterminated string")
	}
	s := p.input[start:p.pos]
	p.pos++
	return s, nil
}

func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case int:
		return t != 0
	default:
		return true
	}
}

func compareValues(left, right any, op string) (bool, error) {
	// Normalize numbers
	lf, lNum := asFloat(left)
	rf, rNum := asFloat(right)
	if lNum && rNum {
		switch op {
		case "==":
			return lf == rf, nil
		case "!=":
			return lf != rf, nil
		case "<":
			return lf < rf, nil
		case "<=":
			return lf <= rf, nil
		case ">":
			return lf > rf, nil
		case ">=":
			return lf >= rf, nil
		}
	}
	ls := templateValueToString(left)
	rs := templateValueToString(right)
	// bool-ish
	if lb, ok := left.(bool); ok {
		if rb, ok2 := right.(bool); ok2 {
			switch op {
			case "==":
				return lb == rb, nil
			case "!=":
				return lb != rb, nil
			}
		}
	}
	switch op {
	case "==":
		return ls == rs, nil
	case "!=":
		return ls != rs, nil
	case "<":
		return ls < rs, nil
	case "<=":
		return ls <= rs, nil
	case ">":
		return ls > rs, nil
	case ">=":
		return ls >= rs, nil
	default:
		return false, fmt.Errorf("unknown operator %s", op)
	}
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// --- Persistence ---

func workflowRunDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "q", "workflow-runs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

func SaveWorkflowRun(run *WorkflowRun) error {
	dir, err := workflowRunDir()
	if err != nil {
		return err
	}
	run.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".run-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceFileAtomic(tempPath, filepath.Join(dir, run.ID+".json"))
}

func LoadWorkflowRun(id string) (*WorkflowRun, error) {
	dir, err := workflowRunDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, filepath.Base(id)+".json"))
	if err != nil {
		return nil, err
	}
	var run WorkflowRun
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, err
	}
	if len(run.Definition.Steps) == 0 {
		return nil, errLegacyWorkflowRun
	}
	// Re-parse duration on resume.
	if d := strings.TrimSpace(run.Definition.Limits.MaxDuration); d != "" {
		if parsed, err := time.ParseDuration(d); err == nil {
			run.Definition.Limits.maxDurationParsed = parsed
		}
	}
	return &run, nil
}

func ListWorkflowRuns() ([]WorkflowRun, error) {
	dir, err := workflowRunDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var runs []WorkflowRun
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			if run, err := LoadWorkflowRun(strings.TrimSuffix(entry.Name(), ".json")); err == nil {
				runs = append(runs, *run)
			}
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].UpdatedAt.After(runs[j].UpdatedAt) })
	return runs, nil
}
