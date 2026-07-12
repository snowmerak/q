package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Workflow struct {
	Version int          `yaml:"version"`
	Name    string       `yaml:"name"`
	Loop    WorkflowLoop `yaml:"loop"`
}

type WorkflowLoop struct {
	MaxIterations  int                  `yaml:"max_iterations"`
	RepairAttempts int                  `yaml:"repair_attempts"`
	Review         WorkflowProviderStep `yaml:"review"`
	Improve        WorkflowProviderStep `yaml:"improve"`
	Verify         *WorkflowCommandStep `yaml:"verify,omitempty"`
}

type WorkflowProviderStep struct {
	Provider string `yaml:"provider"`
	Prompt   string `yaml:"prompt"`
}

type WorkflowCommandStep struct {
	Command string `yaml:"command"`
}

type ReviewOutput struct {
	Done    bool          `json:"done"`
	Summary string        `json:"summary"`
	Issues  []ReviewIssue `json:"issues"`
	Plan    []ReviewPlan  `json:"plan"`
}

type ReviewIssue struct {
	ID                 string `json:"id"`
	Severity           string `json:"severity"`
	Description        string `json:"description"`
	Evidence           string `json:"evidence"`
	AcceptanceCriteria string `json:"acceptance_criteria"`
}

type ReviewPlan struct {
	IssueID string `json:"issue_id"`
	Action  string `json:"action"`
}

type WorkflowRun struct {
	ID           string              `json:"id"`
	WorkflowName string              `json:"workflow_name"`
	WorkflowPath string              `json:"workflow_path"`
	Definition   Workflow            `json:"definition"`
	SessionName  string              `json:"session_name"`
	WorkingDir   string              `json:"working_dir"`
	Input        string              `json:"input"`
	Status       string              `json:"status"`
	Iteration    int                 `json:"iteration"`
	Phase        string              `json:"phase"`
	Iterations   []WorkflowIteration `json:"iterations"`
	LastError    string              `json:"last_error,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

type WorkflowIteration struct {
	Number       int           `json:"number"`
	Status       string        `json:"status"`
	Review       *ReviewOutput `json:"review,omitempty"`
	ReviewRaw    string        `json:"review_raw,omitempty"`
	Improve      string        `json:"improve,omitempty"`
	ImproveDone  bool          `json:"improve_done"`
	VerifyOutput string        `json:"verify_output,omitempty"`
	VerifyError  string        `json:"verify_error,omitempty"`
	VerifyDone   bool          `json:"verify_done"`
}

func LoadWorkflow(path string) (*Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
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
	workflow.Loop.Review.Provider = strings.ToLower(workflow.Loop.Review.Provider)
	workflow.Loop.Improve.Provider = strings.ToLower(workflow.Loop.Improve.Provider)
	if workflow.Version != 1 {
		return fmt.Errorf("unsupported workflow version %d", workflow.Version)
	}
	if strings.TrimSpace(workflow.Name) == "" {
		return errors.New("workflow name is required")
	}
	if workflow.Loop.MaxIterations <= 0 {
		return errors.New("loop.max_iterations must be greater than zero")
	}
	if workflow.Loop.RepairAttempts < 0 {
		return errors.New("loop.repair_attempts cannot be negative")
	}
	if err := validateWorkflowProviderStep("review", workflow.Loop.Review); err != nil {
		return err
	}
	if err := validateWorkflowProviderStep("improve", workflow.Loop.Improve); err != nil {
		return err
	}
	if workflow.Loop.Verify != nil && strings.TrimSpace(workflow.Loop.Verify.Command) == "" {
		return errors.New("loop.verify.command is required")
	}
	return nil
}

func validateWorkflowProviderStep(name string, step WorkflowProviderStep) error {
	provider := strings.ToLower(step.Provider)
	if provider != "codex" && provider != "grok" && provider != "claude" && provider != "agy" {
		return fmt.Errorf("loop.%s.provider must be codex, grok, claude, or agy", name)
	}
	if strings.TrimSpace(step.Prompt) == "" {
		return fmt.Errorf("loop.%s.prompt is required", name)
	}
	return nil
}

func NewWorkflowRun(workflow *Workflow, path, sessionName, input string) (*WorkflowRun, error) {
	id, err := newUUID()
	if err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	workingDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return &WorkflowRun{ID: id, WorkflowName: workflow.Name, WorkflowPath: absPath, Definition: *workflow, SessionName: sessionName,
		WorkingDir: workingDir, Input: input, Status: "pending", Iteration: 1, Phase: "review", CreatedAt: now, UpdatedAt: now}, nil
}

func ExecuteWorkflow(ctx context.Context, workflow *Workflow, run *WorkflowRun, session *Session) error {
	run.Status, run.LastError = "running", ""
	if err := SaveWorkflowRun(run); err != nil {
		return err
	}

	for run.Iteration <= workflow.Loop.MaxIterations {
		iteration := ensureWorkflowIteration(run)
		vars := workflowTemplateVars(run, iteration)

		if iteration.Review == nil {
			run.Phase = "review"
			PrintInfo(fmt.Sprintf("Workflow %s: iteration %d review (%s)", run.ID, run.Iteration, workflow.Loop.Review.Provider))
			if err := SaveWorkflowRun(run); err != nil {
				return err
			}
			prompt := renderWorkflowTemplate(workflow.Loop.Review.Prompt, vars)
			review, raw, err := callStructuredReview(ctx, session, workflow.Loop.Review.Provider, prompt, workflow.Loop.RepairAttempts)
			if err != nil {
				return failWorkflowRun(run, err)
			}
			iteration.Review, iteration.ReviewRaw = review, raw
			if err := SaveWorkflowRun(run); err != nil {
				return err
			}
		}

		if iteration.Review.Done {
			iteration.Status, run.Status, run.Phase = "completed", "completed", "done"
			return SaveWorkflowRun(run)
		}

		vars = workflowTemplateVars(run, iteration)
		if !iteration.ImproveDone {
			run.Phase = "improve"
			PrintInfo(fmt.Sprintf("Workflow %s: iteration %d improve (%s)", run.ID, run.Iteration, workflow.Loop.Improve.Provider))
			if err := SaveWorkflowRun(run); err != nil {
				return err
			}
			prompt := renderWorkflowTemplate(workflow.Loop.Improve.Prompt, vars)
			output, err := callWorkflowProvider(ctx, session, workflow.Loop.Improve.Provider, prompt, "")
			if err != nil {
				return failWorkflowRun(run, err)
			}
			iteration.Improve = output
			iteration.ImproveDone = true
			if err := SaveWorkflowRun(run); err != nil {
				return err
			}
		}

		if workflow.Loop.Verify != nil && !iteration.VerifyDone {
			run.Phase = "verify"
			iteration.VerifyError = ""
			PrintInfo(fmt.Sprintf("Workflow %s: iteration %d verify", run.ID, run.Iteration))
			if err := SaveWorkflowRun(run); err != nil {
				return err
			}
			command := renderWorkflowTemplate(workflow.Loop.Verify.Command, workflowTemplateVars(run, iteration))
			stdout, stderr, finalDir, err := executeCommand(GetSystemInfo().Shell, command, session.PWD)
			iteration.VerifyOutput = string(stdout) + string(stderr)
			if finalDir != "" {
				session.PWD = finalDir
				_ = SaveSession(session)
			}
			if err != nil {
				iteration.VerifyError = err.Error()
				_ = SaveWorkflowRun(run)
				return failWorkflowRun(run, fmt.Errorf("verification failed: %w", err))
			}
			iteration.VerifyDone = true
			if err := SaveWorkflowRun(run); err != nil {
				return err
			}
		}

		iteration.Status = "completed"
		run.Iteration++
		run.Phase = "review"
		if err := SaveWorkflowRun(run); err != nil {
			return err
		}
	}

	err := fmt.Errorf("workflow reached max_iterations=%d without review approval", workflow.Loop.MaxIterations)
	return failWorkflowRun(run, err)
}

func ensureWorkflowIteration(run *WorkflowRun) *WorkflowIteration {
	for i := range run.Iterations {
		if run.Iterations[i].Number == run.Iteration {
			return &run.Iterations[i]
		}
	}
	run.Iterations = append(run.Iterations, WorkflowIteration{Number: run.Iteration, Status: "running"})
	return &run.Iterations[len(run.Iterations)-1]
}

func failWorkflowRun(run *WorkflowRun, err error) error {
	run.Status, run.LastError = "failed", err.Error()
	_ = SaveWorkflowRun(run)
	return err
}

func callStructuredReview(ctx context.Context, session *Session, provider, prompt string, repairs int) (*ReviewOutput, string, error) {
	schemaValue := reviewOutputSchema()
	schemaJSON, _ := json.Marshal(schemaValue)
	currentPrompt := prompt
	var raw string
	for attempt := 0; attempt <= repairs; attempt++ {
		var err error
		raw, err = callWorkflowProvider(ctx, session, provider, currentPrompt, string(schemaJSON))
		if err != nil {
			return nil, raw, err
		}
		payload, err := extractStructuredPayload(provider, raw)
		if err == nil {
			var review ReviewOutput
			if err = json.Unmarshal(payload, &review); err == nil {
				err = validateReviewOutput(&review)
			}
			if err == nil {
				return &review, string(payload), nil
			}
		}
		if attempt == repairs {
			return nil, raw, fmt.Errorf("invalid structured review from %s: %w", provider, err)
		}
		currentPrompt = "Your previous response failed structured-output validation. Do not use tools. Return only a corrected JSON object matching the requested schema. Validation error: " + err.Error()
	}
	return nil, raw, errors.New("unreachable structured review state")
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
	var wrapper struct {
		StructuredOutput      json.RawMessage `json:"structuredOutput"`
		StructuredOutputSnake json.RawMessage `json:"structured_output"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		return nil, err
	}
	payload := wrapper.StructuredOutput
	if provider == "claude" {
		payload = wrapper.StructuredOutputSnake
	}
	if len(payload) == 0 || string(payload) == "null" {
		return nil, errors.New("structured output field is missing")
	}
	return payload, nil
}

func validateReviewOutput(review *ReviewOutput) error {
	if strings.TrimSpace(review.Summary) == "" {
		return errors.New("summary is required")
	}
	if review.Done && len(review.Issues) > 0 {
		return errors.New("done=true cannot include issues")
	}
	if !review.Done && len(review.Issues) == 0 {
		return errors.New("done=false must include at least one issue")
	}
	ids := make(map[string]bool)
	for _, issue := range review.Issues {
		if issue.ID == "" || issue.Description == "" || issue.Evidence == "" || issue.AcceptanceCriteria == "" {
			return errors.New("issue fields cannot be empty")
		}
		if issue.Severity != "low" && issue.Severity != "medium" && issue.Severity != "high" {
			return fmt.Errorf("invalid severity %q", issue.Severity)
		}
		ids[issue.ID] = true
	}
	planned := make(map[string]bool)
	for _, plan := range review.Plan {
		if !ids[plan.IssueID] || plan.Action == "" {
			return errors.New("plan must reference an issue and include an action")
		}
		planned[plan.IssueID] = true
	}
	for id := range ids {
		if !planned[id] {
			return fmt.Errorf("issue %q has no improvement plan", id)
		}
	}
	return nil
}

func reviewOutputSchema() map[string]any {
	return map[string]any{"type": "object", "required": []string{"done", "summary", "issues", "plan"}, "additionalProperties": false,
		"properties": map[string]any{
			"done": map[string]any{"type": "boolean"}, "summary": map[string]any{"type": "string", "minLength": 1},
			"issues": map[string]any{"type": "array", "items": map[string]any{"type": "object", "required": []string{"id", "severity", "description", "evidence", "acceptance_criteria"}, "additionalProperties": false, "properties": map[string]any{"id": map[string]any{"type": "string"}, "severity": map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}}, "description": map[string]any{"type": "string"}, "evidence": map[string]any{"type": "string"}, "acceptance_criteria": map[string]any{"type": "string"}}}},
			"plan":   map[string]any{"type": "array", "items": map[string]any{"type": "object", "required": []string{"issue_id", "action"}, "additionalProperties": false, "properties": map[string]any{"issue_id": map[string]any{"type": "string"}, "action": map[string]any{"type": "string"}}}},
		}}
}

func workflowTemplateVars(run *WorkflowRun, iteration *WorkflowIteration) map[string]string {
	review, _ := json.Marshal(iteration.Review)
	return map[string]string{"input": run.Input, "iteration": strconv.Itoa(iteration.Number), "review": string(review), "improve": iteration.Improve, "verify": iteration.VerifyOutput}
}

func renderWorkflowTemplate(template string, vars map[string]string) string {
	for key, value := range vars {
		template = strings.ReplaceAll(template, "{{"+key+"}}", value)
		template = strings.ReplaceAll(template, "{{ "+key+" }}", value)
	}
	return template
}

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
