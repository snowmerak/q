package subagent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

const (
	CoderCompleteToolName = "task_complete"
	defaultCoderRounds    = 48
	maximumCoderReminders = 3
	maximumCoderListItems = 32
	maximumCoderTextBytes = 32 << 10
)

// CoderRunner executes one approved plan task in an isolated model/tool
// session. It exposes the workspace runtime catalog plus task_complete; it
// does not expose user interaction or subagent delegation controls.
type CoderRunner struct {
	Client           AgentClient
	Tools            ToolRuntime
	Spec             Spec
	Sink             RecordSink
	RunID            string
	ExecutionID      string
	WorkingDirectory string
	Environment      string
	MaxRounds        int
	Progress         ProgressFunc
	Trace            TraceFunc
}

func (r CoderRunner) Run(ctx context.Context, attempt CoderAttempt) (result CoderResult, runErr error) {
	if ctx == nil {
		return CoderResult{}, errors.New("subagent: coder context is nil")
	}
	if r.Client == nil || r.Tools == nil {
		return CoderResult{}, errors.New("subagent: coder requires client and tools")
	}
	if r.Spec.Role != config.AgentRoleCoder {
		return CoderResult{}, fmt.Errorf("subagent: coder runner requires role %q", config.AgentRoleCoder)
	}
	prompt, err := CoderSystemPrompt(attempt.Plan, attempt.TaskIndex, attempt.Attempt, attempt.Targets, attempt.Feedback)
	if err != nil {
		return CoderResult{}, err
	}
	if environment := strings.TrimSpace(r.Environment); environment != "" {
		prompt += "\n\n" + environment
	}
	taskID := r.coderTaskID(attempt)
	var lifecycle *Lifecycle
	if r.Sink != nil || strings.TrimSpace(r.RunID) != "" {
		lifecycle, err = NewLifecycle(r.Sink, r.RunID, taskID, r.ExecutionID, r.Spec)
		if err != nil {
			return CoderResult{}, err
		}
		if err := lifecycle.Queued(prompt); err != nil {
			return CoderResult{}, err
		}
		if err := lifecycle.Started(prompt); err != nil {
			return CoderResult{}, err
		}
	}
	result, runErr = r.run(ctx, attempt, taskID, prompt, lifecycle)
	if runErr != nil {
		if lifecycle != nil {
			runErr = errors.Join(runErr, lifecycle.Failed(runErr))
		}
		return CoderResult{}, runErr
	}
	if lifecycle != nil {
		if err := lifecycle.Succeeded(result.Summary, result.Summary, result); err != nil {
			return CoderResult{}, err
		}
	}
	return result, nil
}

func (r CoderRunner) run(
	ctx context.Context,
	attempt CoderAttempt,
	taskID string,
	prompt string,
	lifecycle *Lifecycle,
) (CoderResult, error) {
	messages := []client.Message{
		{Role: client.RoleSystem, Content: prompt},
		{Role: client.RoleUser, Content: fmt.Sprintf(
			"Execute approved task %d of %d now. Finish with task_complete.",
			attempt.TaskIndex+1, len(attempt.Plan.Steps),
		)},
	}
	available := coderTools(r.Tools.Tools())
	rounds := r.MaxRounds
	if rounds <= 0 {
		rounds = defaultCoderRounds
	}
	reminders := 0
	for round := 0; round < rounds; round++ {
		reportProgress(r.Progress, ProgressEvent{
			Agent: "coder", TaskID: taskID, ParentID: r.ExecutionID,
			Action: ProgressThinking, Detail: fmt.Sprintf("model round %d", round+1),
		})
		parallel := false
		request := client.ChatRequest{
			Messages: messages, Tools: available, ToolChoice: client.ToolChoiceAuto,
			ParallelToolCalls: &parallel, WorkingDirectory: r.WorkingDirectory,
		}
		if reminders > 0 {
			request.ToolChoice = client.NamedToolChoice(CoderCompleteToolName)
		}
		r.Spec.Apply(&request)
		response, err := r.Client.Chat(ctx, request)
		if err != nil {
			return CoderResult{}, fmt.Errorf("subagent: coder model: %w", err)
		}
		assistant, err := scoutAssistantMessage(response)
		if err != nil {
			return CoderResult{}, fmt.Errorf("subagent: coder: %w", err)
		}
		messages = append(messages, assistant)
		traceAssistant(r.Trace, "coder", taskID, r.ExecutionID, assistant)
		if lifecycle != nil {
			if err := lifecycle.Message(assistant); err != nil {
				return CoderResult{}, err
			}
		}
		if len(assistant.ToolCalls) == 0 {
			if reminders == maximumCoderReminders {
				return CoderResult{}, errors.New("subagent: coder ended without task_complete")
			}
			reminders++
			messages = append(messages, client.Message{Role: client.RoleSystem, Content: fmt.Sprintf(
				"Reminder %d/%d: finish this Coder attempt by calling task_complete now; do not answer with plain text.",
				reminders, maximumCoderReminders,
			)})
			continue
		}
		for _, call := range assistant.ToolCalls {
			reportProgress(r.Progress, ProgressEvent{
				Agent: "coder", TaskID: taskID, ParentID: r.ExecutionID,
				Action: ProgressTool, Detail: call.Function.Name,
			})
			var toolResult client.ToolResult
			if call.Function.Name == CoderCompleteToolName {
				if len(assistant.ToolCalls) != 1 {
					toolResult = scoutToolError(errors.New("task_complete must be the only tool call in its turn"))
				} else {
					completion, err := parseCoderCompletion(call.Function.Arguments)
					if err == nil {
						return completion, nil
					}
					toolResult = scoutToolError(err)
				}
			} else if !hasTool(available, call.Function.Name) {
				toolResult = scoutToolError(fmt.Errorf("tool %q is not available to coder", call.Function.Name))
			} else {
				toolResult, err = r.Tools.Call(ctx, call)
				if err != nil {
					toolResult = scoutToolError(err)
				}
			}
			message := client.Message{
				Role: client.RoleTool, Name: call.Function.Name,
				ToolCallID: call.ID, Content: toolResult.Content,
			}
			traceToolResult(r.Trace, "coder", taskID, r.ExecutionID, call.Function.Name, toolResult)
			messages = append(messages, message)
			if lifecycle != nil {
				if err := lifecycle.Message(message); err != nil {
					return CoderResult{}, err
				}
			}
		}
	}
	return CoderResult{}, fmt.Errorf("subagent: coder exceeded %d model rounds", rounds)
}

func (r CoderRunner) coderTaskID(attempt CoderAttempt) string {
	prefix := strings.TrimSpace(r.ExecutionID)
	if prefix == "" {
		prefix = "plan-execution"
	}
	return fmt.Sprintf("%s-coder-%d-%d", prefix, attempt.TaskIndex+1, attempt.Attempt)
}

func coderTools(available []client.Tool) []client.Tool {
	result := make([]client.Tool, 0, len(available)+1)
	seen := make(map[string]struct{}, len(available)+1)
	for _, tool := range available {
		name := strings.TrimSpace(tool.Function.Name)
		if name == "" || coderReservedTool(name) {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, tool)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Function.Name < result[j].Function.Name })
	result = append(result, coderCompletionTool())
	return result
}

func coderReservedTool(name string) bool {
	switch name {
	case CoderCompleteToolName, "task_start", AskToUserToolName, DelegateScoutToolName,
		SubmitBriefToolName, SubmitPlanToolName, ReviewTaskToolName, "cmd_status", "search_skills", "get_skill":
		return true
	default:
		return false
	}
}

func coderCompletionTool() client.Tool {
	strict := true
	stringsSchema := map[string]any{
		"type": "array", "maxItems": maximumCoderListItems,
		"items": map[string]any{"type": "string"},
	}
	return client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
		Name: CoderCompleteToolName, Description: "Finish this Coder attempt with its structured implementation and verification result.", Strict: &strict,
		Parameters: map[string]any{
			"type": "object", "properties": map[string]any{
				"outcome": map[string]any{"type": "string", "enum": []string{"succeeded", "blocked"}},
				"summary": map[string]any{"type": "string"}, "findings": stringsSchema,
				"artifacts": stringsSchema, "verification": stringsSchema,
				"blocker": map[string]any{"type": "string"},
			}, "required": []string{"outcome", "summary"}, "additionalProperties": false,
		},
	}}
}

func parseCoderCompletion(arguments string) (CoderResult, error) {
	var result CoderResult
	if err := decodeStrict(arguments, &result); err != nil {
		return CoderResult{}, fmt.Errorf("decode task_complete: %w", err)
	}
	result.Outcome = strings.TrimSpace(result.Outcome)
	result.Summary = strings.TrimSpace(result.Summary)
	result.Blocker = strings.TrimSpace(result.Blocker)
	result.Findings = cleanStrings(result.Findings)
	result.Artifacts = cleanStrings(result.Artifacts)
	result.Verification = cleanStrings(result.Verification)
	if err := validateCoderResult(result); err != nil {
		return CoderResult{}, err
	}
	return result, nil
}

func validateCoderResult(result CoderResult) error {
	if result.Outcome != "succeeded" && result.Outcome != "blocked" {
		return errors.New("task_complete outcome must be succeeded or blocked")
	}
	if result.Summary == "" {
		return errors.New("task_complete summary is required")
	}
	if len(result.Summary) > maximumCoderTextBytes || len(result.Blocker) > maximumCoderTextBytes {
		return fmt.Errorf("task_complete summary and blocker must not exceed %d bytes", maximumCoderTextBytes)
	}
	if result.Outcome == "blocked" && result.Blocker == "" {
		return errors.New("task_complete blocker is required for blocked outcome")
	}
	if len(result.Findings) > maximumCoderListItems || len(result.Artifacts) > maximumCoderListItems ||
		len(result.Verification) > maximumCoderListItems {
		return fmt.Errorf("task_complete lists must contain at most %d items", maximumCoderListItems)
	}
	return nil
}

func hasTool(tools []client.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}
