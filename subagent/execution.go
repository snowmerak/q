package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/loom"
)

const (
	TargetSelectorPaths = "paths"
	TargetSelectorLoom  = "loom"
	ReviewTaskToolName  = "review_task"
	maximumReviewRounds = 8
)

// TargetCondition is a disjunctive normal form over file-set selectors.
// Any is a union (OR); every All group is an intersection (AND). Selectors in
// one task are independent and never consume results from another task.
type TargetCondition struct {
	Any []TargetProduct `json:"any"`
}

type TargetProduct struct {
	All []TargetSelector `json:"all"`
}

type TargetSelector struct {
	Kind   string            `json:"kind"`
	Paths  []string          `json:"paths,omitempty"`
	Code   string            `json:"code,omitempty"`
	Inputs map[string]string `json:"inputs,omitempty"`
}

type CoderResult struct {
	Outcome      string          `json:"outcome"`
	Summary      string          `json:"summary"`
	Findings     []string        `json:"findings,omitempty"`
	Artifacts    []string        `json:"artifacts,omitempty"`
	Verification []string        `json:"verification,omitempty"`
	Blocker      string          `json:"blocker,omitempty"`
	Evidence     []CoderEvidence `json:"evidence,omitempty"`
}

// CoderEvidence is the bounded, automatically collected proof for one Coder
// tool call. It deliberately omits tool arguments and result bodies: reviewers
// receive only the tool identity, its immutable Loom receipt, error state, and
// workspace paths relevant to file access.
type CoderEvidence struct {
	Tool    string   `json:"tool"`
	LoomRef string   `json:"loom_ref,omitempty"`
	IsError bool     `json:"is_error"`
	Paths   []string `json:"paths,omitempty"`
}

type TaskReviewRequest struct {
	Plan      PlanProposal `json:"plan"`
	TaskIndex int          `json:"task_index"`
	Attempt   int          `json:"attempt"`
	Targets   []string     `json:"resolved_targets,omitempty"`
	Result    CoderResult  `json:"result"`
}

// TaskReview has only two transitions. Feedback is always present so retry
// with and without additional guidance share the same shape.
type TaskReview struct {
	Decision string   `json:"decision"`
	Feedback string   `json:"feedback"`
	Facts    []string `json:"facts,omitempty"`
}

type PlannerReviewRunner struct {
	Client           AgentClient
	Tools            ToolRuntime
	Spec             Spec
	Sink             RecordSink
	RunID            string
	ExecutionID      string
	WorkingDirectory string
	MaxRounds        int
	Progress         ProgressFunc
	Trace            TraceFunc
}

func (r PlannerReviewRunner) Run(ctx context.Context, input TaskReviewRequest) (review TaskReview, runErr error) {
	if ctx == nil {
		return TaskReview{}, errors.New("subagent: planner review context is nil")
	}
	if r.Client == nil {
		return TaskReview{}, errors.New("subagent: planner review client is required")
	}
	if r.Spec.Role != config.AgentRolePlanner {
		return TaskReview{}, fmt.Errorf("subagent: planner review requires role %q", config.AgentRolePlanner)
	}
	if err := validateTaskReviewRequest(input); err != nil {
		return TaskReview{}, err
	}
	body, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return TaskReview{}, err
	}
	prompt := "Review this completed Coder attempt.\n\n" + string(body)
	taskID := fmt.Sprintf("%s-planner-%d-%d", strings.TrimSpace(r.ExecutionID), input.TaskIndex+1, input.Attempt)
	if strings.TrimSpace(r.ExecutionID) == "" {
		taskID = fmt.Sprintf("plan-execution-planner-%d-%d", input.TaskIndex+1, input.Attempt)
	}
	var lifecycle *Lifecycle
	if r.Sink != nil || strings.TrimSpace(r.RunID) != "" {
		lifecycle, err = NewLifecycle(r.Sink, r.RunID, taskID, r.ExecutionID, r.Spec)
		if err != nil {
			return TaskReview{}, err
		}
		if err := lifecycle.Queued(prompt); err != nil {
			return TaskReview{}, err
		}
		if err := lifecycle.Started(prompt); err != nil {
			return TaskReview{}, err
		}
	}
	defer func() {
		if runErr != nil && lifecycle != nil {
			runErr = errors.Join(runErr, lifecycle.Failed(runErr))
		}
	}()
	messages := []client.Message{
		{Role: client.RoleSystem, Content: plannerReviewInstructions()},
		{Role: client.RoleUser, Content: prompt},
	}
	rounds := r.MaxRounds
	if rounds <= 0 {
		rounds = maximumReviewRounds
	}
	reportProgress(r.Progress, ProgressEvent{
		Agent: "planner", TaskID: taskID, ParentID: r.ExecutionID,
		Action: ProgressStarted, Detail: "reviewing Coder result",
	})
	for round := 0; round < rounds; round++ {
		reportProgress(r.Progress, ProgressEvent{
			Agent: "planner", TaskID: taskID, ParentID: r.ExecutionID,
			Action: ProgressThinking, Detail: fmt.Sprintf("review round %d", round+1),
		})
		available := plannerReviewTools(nil)
		if r.Tools != nil {
			available = plannerReviewTools(r.Tools.Tools())
		}
		parallel := false
		request := client.ChatRequest{
			Messages: messages, Tools: available,
			ToolChoice: client.ToolChoiceAuto, ParallelToolCalls: &parallel,
			WorkingDirectory: r.WorkingDirectory,
		}
		if len(available) == 1 {
			request.ToolChoice = client.NamedToolChoice(ReviewTaskToolName)
		}
		r.Spec.Apply(&request)
		response, err := r.Client.Chat(ctx, request)
		if err != nil {
			return TaskReview{}, fmt.Errorf("subagent: planner review model: %w", err)
		}
		assistant, err := scoutAssistantMessage(response)
		if err != nil {
			return TaskReview{}, fmt.Errorf("subagent: planner review: %w", err)
		}
		messages = append(messages, assistant)
		traceAssistant(r.Trace, "planner", taskID, r.ExecutionID, assistant)
		if lifecycle != nil {
			if err := lifecycle.Message(assistant); err != nil {
				return TaskReview{}, err
			}
		}
		if len(assistant.ToolCalls) == 0 {
			messages = append(messages, client.Message{Role: client.RoleSystem, Content: "Inspect evidence only when needed, then finish by calling review_task exactly once."})
			continue
		}
		if len(assistant.ToolCalls) == 1 && assistant.ToolCalls[0].Function.Name == ReviewTaskToolName {
			call := assistant.ToolCalls[0]
			reportProgress(r.Progress, ProgressEvent{
				Agent: "planner", TaskID: taskID, ParentID: r.ExecutionID,
				Action: ProgressTool, Detail: ReviewTaskToolName,
			})
			review, err = parseTaskReview(call.Function.Arguments)
			if err == nil {
				if lifecycle != nil {
					if err := lifecycle.Succeeded(review.Decision, review.Decision, review); err != nil {
						return TaskReview{}, err
					}
				}
				reportProgress(r.Progress, ProgressEvent{
					Agent: "planner", TaskID: taskID, ParentID: r.ExecutionID,
					Action: ProgressCompleted, Detail: review.Decision,
				})
				return review, nil
			}
			message := client.Message{
				Role: client.RoleTool, Name: ReviewTaskToolName, ToolCallID: assistant.ToolCalls[0].ID,
				Content: scoutToolError(err).Content,
			}
			traceToolResult(r.Trace, "planner", taskID, r.ExecutionID, ReviewTaskToolName, scoutToolError(err))
			messages = append(messages, message)
			if lifecycle != nil {
				if err := lifecycle.Message(message); err != nil {
					return TaskReview{}, err
				}
			}
			continue
		}
		for _, call := range assistant.ToolCalls {
			reportProgress(r.Progress, ProgressEvent{
				Agent: "planner", TaskID: taskID, ParentID: r.ExecutionID,
				Action: ProgressTool, Detail: call.Function.Name,
			})
			var toolResult client.ToolResult
			switch {
			case call.Function.Name == ReviewTaskToolName:
				toolResult = scoutToolError(errors.New("review_task must be the only tool call in its turn"))
			case r.Tools == nil || !hasTool(available, call.Function.Name):
				toolResult = scoutToolError(fmt.Errorf("tool %q is not available to planner review", call.Function.Name))
			default:
				toolResult, err = r.Tools.Call(ctx, call)
				if err != nil {
					toolResult = scoutToolError(err)
				}
			}
			message := client.Message{
				Role: client.RoleTool, Name: call.Function.Name, ToolCallID: call.ID,
				Content: toolResult.Content,
			}
			traceToolResult(r.Trace, "planner", taskID, r.ExecutionID, call.Function.Name, toolResult)
			messages = append(messages, message)
			if lifecycle != nil {
				if err := lifecycle.Message(message); err != nil {
					return TaskReview{}, err
				}
			}
		}
	}
	return TaskReview{}, fmt.Errorf("subagent: planner review exceeded %d model rounds", rounds)
}

// CoderSystemPrompt embeds the complete current plan on every attempt. Facts
// learned by Planner reviews therefore reach the next Coder invocation.
func CoderSystemPrompt(plan PlanProposal, taskIndex, attempt int, targets []string, feedback string) (string, error) {
	if taskIndex < 0 || taskIndex >= len(plan.Steps) {
		return "", fmt.Errorf("subagent: coder task index %d is out of range", taskIndex)
	}
	if err := validateTargetCondition(&plan.Steps[taskIndex].Target); err != nil {
		return "", fmt.Errorf("subagent: coder task target: %w", err)
	}
	payload := struct {
		Plan      PlanProposal `json:"plan"`
		TaskIndex int          `json:"task_index"`
		Attempt   int          `json:"attempt"`
		Targets   []string     `json:"resolved_targets"`
		Feedback  string       `json:"feedback"`
	}{plan, taskIndex, max(1, attempt), cleanStrings(targets), feedback}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return `You are q's isolated Coder subagent. Execute only the current task from the approved plan below. The complete current plan is authoritative context and may include facts learned from earlier tasks. Do not change another task, broaden scope, or reinterpret target conditions. Use the resolved target files, perform appropriate verification, and finish with task_complete. On each tool-calling turn, include a concise user-visible progress note describing your immediate intent and what the call should establish; do not expose or invent hidden chain-of-thought.

run_command is asynchronous. When it returns a command_id, use wait with the latest next_offset until the command finishes. If wait still reports running, call wait again with its next_offset. Never busy-poll command status through repeated model rounds.

` + string(body), nil
}

func ApplyTaskReview(plan *PlanProposal, review TaskReview) error {
	if plan == nil {
		return errors.New("subagent: plan is required")
	}
	if err := validateTaskReview(review); err != nil {
		return err
	}
	plan.Facts = cleanStrings(append(plan.Facts, review.Facts...))
	return nil
}

func validateTargetCondition(target *TargetCondition) error {
	if target == nil || len(target.Any) == 0 {
		return errors.New("target requires at least one any group")
	}
	if len(target.Any) > 16 {
		return errors.New("target accepts at most 16 any groups")
	}
	for groupIndex := range target.Any {
		group := &target.Any[groupIndex]
		if len(group.All) == 0 {
			return fmt.Errorf("any group %d requires at least one all selector", groupIndex+1)
		}
		if len(group.All) > 16 {
			return fmt.Errorf("any group %d accepts at most 16 selectors", groupIndex+1)
		}
		for selectorIndex := range group.All {
			selector := &group.All[selectorIndex]
			selector.Kind = strings.TrimSpace(selector.Kind)
			selector.Paths = cleanStrings(selector.Paths)
			selector.Code = strings.TrimSpace(selector.Code)
			switch selector.Kind {
			case TargetSelectorPaths:
				if len(selector.Paths) == 0 || selector.Code != "" || len(selector.Inputs) != 0 {
					return fmt.Errorf("selector %d.%d paths kind requires only non-empty paths", groupIndex+1, selectorIndex+1)
				}
				for _, path := range selector.Paths {
					if !workspaceRelativePath(path) {
						return fmt.Errorf("selector %d.%d path %q must stay workspace-relative", groupIndex+1, selectorIndex+1, path)
					}
				}
			case TargetSelectorLoom:
				if selector.Code == "" || len(selector.Paths) != 0 || len(selector.Inputs) == 0 {
					return fmt.Errorf("selector %d.%d loom kind requires code, inputs, and no paths", groupIndex+1, selectorIndex+1)
				}
				if len(selector.Code) > 256<<10 {
					return fmt.Errorf("selector %d.%d Loom code exceeds 262144 bytes", groupIndex+1, selectorIndex+1)
				}
				for name, value := range selector.Inputs {
					cleanName, cleanValue := strings.TrimSpace(name), strings.TrimSpace(value)
					if cleanName == "" {
						return fmt.Errorf("selector %d.%d Loom input name is required", groupIndex+1, selectorIndex+1)
					}
					if _, err := loom.ParseRef(cleanValue); err != nil {
						return fmt.Errorf("selector %d.%d Loom input %q: %w", groupIndex+1, selectorIndex+1, name, err)
					}
					if cleanName != name {
						delete(selector.Inputs, name)
					}
					selector.Inputs[cleanName] = cleanValue
				}
			default:
				return fmt.Errorf("selector %d.%d kind must be %q or %q", groupIndex+1, selectorIndex+1, TargetSelectorPaths, TargetSelectorLoom)
			}
		}
	}
	return nil
}

func workspaceRelativePath(value string) bool {
	value = strings.TrimSpace(value)
	normalized := strings.ReplaceAll(value, "\\", "/")
	if value == "" || strings.HasPrefix(normalized, "/") ||
		(len(normalized) >= 2 && normalized[1] == ':') ||
		filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
		return false
	}
	cleaned := filepath.Clean(value)
	return cleaned != ".." && !strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) &&
		!strings.HasPrefix(strings.ReplaceAll(cleaned, "\\", "/"), "../")
}

func renderTargetCondition(target TargetCondition) string {
	groups := make([]string, 0, len(target.Any))
	for _, group := range target.Any {
		selectors := make([]string, 0, len(group.All))
		for _, selector := range group.All {
			if selector.Kind == TargetSelectorPaths {
				selectors = append(selectors, "paths("+strings.Join(selector.Paths, ", ")+")")
			} else {
				selectors = append(selectors, fmt.Sprintf("loom(%d inputs)", len(selector.Inputs)))
			}
		}
		groups = append(groups, "("+strings.Join(selectors, " AND ")+")")
	}
	return strings.Join(groups, " OR ")
}

func validateTaskReviewRequest(request TaskReviewRequest) error {
	if request.TaskIndex < 0 || request.TaskIndex >= len(request.Plan.Steps) {
		return fmt.Errorf("subagent: planner review task index %d is out of range", request.TaskIndex)
	}
	if request.Attempt <= 0 {
		return errors.New("subagent: planner review attempt must be positive")
	}
	if len(request.Targets) > 0 {
		if err := validateResolvedTargets(request.Targets); err != nil {
			return fmt.Errorf("subagent: planner review targets: %w", err)
		}
	}
	request.Result.Outcome = strings.TrimSpace(request.Result.Outcome)
	request.Result.Summary = strings.TrimSpace(request.Result.Summary)
	request.Result.Blocker = strings.TrimSpace(request.Result.Blocker)
	if err := validateCoderResult(request.Result); err != nil {
		return fmt.Errorf("subagent: Coder result: %w", err)
	}
	return nil
}

func parseTaskReview(arguments string) (TaskReview, error) {
	var wire struct {
		Decision string   `json:"decision"`
		Feedback *string  `json:"feedback"`
		Facts    []string `json:"facts,omitempty"`
	}
	if err := decodeStrict(arguments, &wire); err != nil {
		return TaskReview{}, err
	}
	if wire.Feedback == nil {
		return TaskReview{}, errors.New("review_task feedback is required; use an empty string when no feedback is needed")
	}
	review := TaskReview{
		Decision: strings.TrimSpace(wire.Decision), Feedback: strings.TrimSpace(*wire.Feedback), Facts: cleanStrings(wire.Facts),
	}
	if err := validateTaskReview(review); err != nil {
		return TaskReview{}, err
	}
	return review, nil
}

func validateTaskReview(review TaskReview) error {
	if review.Decision != "retry" && review.Decision != "next" {
		return errors.New("review_task decision must be retry or next")
	}
	return nil
}

func plannerReviewInstructions() string {
	return `You are q's Planner reviewing one Coder attempt against the current approved plan. The plan is a task list, not a dataflow graph. Target conditions select files for one task only; there are no sequential conditions or result conditions.

The Coder result includes bounded evidence collected automatically from its tool calls: tool identity, Loom reference, error state, and relevant workspace paths. It does not include raw command output or the full Coder transcript. Inspect current files or selected Loom evidence only when needed. Use run_command only for non-mutating verification and follow it with wait; do not run Git commands or modify the workspace.

Choose exactly one transition:
- retry: run the same task again. Always provide feedback; use an empty string when no additional guidance is needed.
- next: accept this task and advance to the next task (or finish after the final task).

Add only durable facts newly learned from the attempt to facts. Those facts become part of the current plan before the next Coder invocation. Include a concise user-visible review note with the tool call, without exposing hidden chain-of-thought. Finish by calling review_task exactly once.`
}

func plannerReviewTools(available []client.Tool) []client.Tool {
	allowed := map[string]struct{}{
		"read_file": {}, "loom_inspect": {}, "loom_read": {}, "loom_eval": {},
		"run_command": {}, "wait": {},
	}
	result := make([]client.Tool, 0, len(allowed)+1)
	seen := make(map[string]struct{}, len(allowed)+1)
	for _, tool := range available {
		name := strings.TrimSpace(tool.Function.Name)
		_, explicitlyAllowed := allowed[name]
		if !explicitlyAllowed && !lspQueryToolAllowed(name) {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, tool)
	}
	result = append(result, reviewTaskTool())
	return result
}

func reviewTaskTool() client.Tool {
	strict := true
	return client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
		Name: ReviewTaskToolName, Description: "Review one Coder attempt and choose retry or next.", Strict: &strict,
		Parameters: map[string]any{
			"type": "object", "properties": map[string]any{
				"decision": map[string]any{"type": "string", "enum": []string{"retry", "next"}},
				"feedback": map[string]any{"type": "string"},
				"facts":    stringArraySchemaValue(),
			}, "required": []string{"decision", "feedback"}, "additionalProperties": false,
		},
	}}
}
