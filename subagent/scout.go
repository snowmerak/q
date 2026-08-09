package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/sessionstore"
)

const (
	ScoutCompleteToolName = "task_complete"
	defaultScoutRounds    = 24
	maximumScoutReminders = 3
	maximumScoutFindings  = 32
	maximumScoutListItems = 16
	maximumScoutTextBytes = 16 << 10
)

var scoutReadTools = map[string]struct{}{
	"read_file":      {},
	"list_directory": {},
	"loom_inspect":   {},
	"loom_read":      {},
	"loom_eval":      {},
}

// AgentClient is the model surface required by an isolated subagent run.
type AgentClient interface {
	Chat(context.Context, client.ChatRequest) (*client.ChatResponse, error)
}

// ToolRuntime supplies workspace tools. ScoutRunner filters this catalog to
// its read-only allowlist before exposing it to the model.
type ToolRuntime interface {
	Tools() []client.Tool
	Call(context.Context, client.ToolCall) (client.ToolResult, error)
}

// ScoutTask is a bounded repository question delegated by Griller while a
// Grill loop is still active. CandidateFiles and LoomInputs are optional leads;
// discovering the actual relevant files may itself be the scout objective.
type ScoutTask struct {
	ID                 string     `json:"id,omitempty"`
	ParentID           string     `json:"parent_id,omitempty"`
	Objective          string     `json:"objective"`
	CompletionCriteria []string   `json:"completion_criteria,omitempty"`
	CandidateFiles     []string   `json:"candidate_files,omitempty"`
	LoomInputs         []loom.Ref `json:"loom_inputs,omitempty"`
	Context            []string   `json:"context,omitempty"`
}

type ScoutFinding struct {
	Path     string   `json:"path,omitempty"`
	Symbol   string   `json:"symbol,omitempty"`
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence,omitempty"`
	Risks    []string `json:"risks,omitempty"`
}

type ScoutResult struct {
	TaskID       string         `json:"task_id"`
	Status       string         `json:"status"`
	Outcome      string         `json:"outcome"`
	Summary      string         `json:"summary"`
	Findings     []ScoutFinding `json:"findings,omitempty"`
	Artifacts    []string       `json:"artifacts,omitempty"`
	Verification []string       `json:"verification,omitempty"`
	Blocker      string         `json:"blocker,omitempty"`
	Usage        client.Usage   `json:"usage,omitempty"`
}

type scoutCompletion struct {
	Outcome      string         `json:"outcome"`
	Summary      string         `json:"summary"`
	Findings     []ScoutFinding `json:"findings,omitempty"`
	Artifacts    []string       `json:"artifacts,omitempty"`
	Verification []string       `json:"verification,omitempty"`
	Blocker      string         `json:"blocker,omitempty"`
}

// ScoutRunner executes one isolated, read-only repository investigation.
// Sink and RunID are optional together; when configured they persist the
// lifecycle through the existing durable archive.
type ScoutRunner struct {
	Client           AgentClient
	Tools            ToolRuntime
	Spec             Spec
	Sink             RecordSink
	RunID            string
	WorkingDirectory string
	MaxRounds        int
	Progress         ProgressFunc
}

func (r ScoutRunner) Run(ctx context.Context, task ScoutTask) (result ScoutResult, runErr error) {
	if ctx == nil {
		return ScoutResult{}, errors.New("subagent: scout context is nil")
	}
	if r.Client == nil {
		return ScoutResult{}, errors.New("subagent: scout client is required")
	}
	if r.Tools == nil {
		return ScoutResult{}, errors.New("subagent: scout tool runtime is required")
	}
	if r.Spec.Role != config.AgentRoleScout {
		return ScoutResult{}, fmt.Errorf("subagent: scout runner requires role %q", config.AgentRoleScout)
	}
	prepared, err := normalizeScoutTask(task)
	if err != nil {
		return ScoutResult{}, err
	}
	reportProgress(r.Progress, ProgressEvent{
		Agent: "scout", TaskID: prepared.ID, ParentID: prepared.ParentID,
		Action: ProgressStarted, Detail: prepared.Objective,
	})
	defer func() {
		if runErr != nil {
			reportProgress(r.Progress, ProgressEvent{
				Agent: "scout", TaskID: prepared.ID, ParentID: prepared.ParentID,
				Action: ProgressFailed, Detail: runErr.Error(),
			})
			return
		}
		reportProgress(r.Progress, ProgressEvent{
			Agent: "scout", TaskID: prepared.ID, ParentID: prepared.ParentID,
			Action: ProgressCompleted, Detail: result.Summary,
		})
	}()

	prompt, err := encodeScoutTask(prepared)
	if err != nil {
		return ScoutResult{}, err
	}
	var lifecycle *Lifecycle
	if r.Sink != nil || strings.TrimSpace(r.RunID) != "" {
		lifecycle, err = NewLifecycle(r.Sink, r.RunID, prepared.ID, prepared.ParentID, r.Spec)
		if err != nil {
			return ScoutResult{}, err
		}
		if err := lifecycle.Queued(prompt); err != nil {
			return ScoutResult{}, err
		}
		if err := lifecycle.Started(prompt); err != nil {
			return ScoutResult{}, err
		}
	}

	result, runErr = r.run(ctx, prepared, prompt, lifecycle)
	if runErr != nil {
		if lifecycle != nil {
			runErr = errors.Join(runErr, lifecycle.Failed(runErr))
		}
		return ScoutResult{}, runErr
	}
	if lifecycle != nil {
		if err := lifecycle.Succeeded(result.Summary, result.Summary, result); err != nil {
			return ScoutResult{}, err
		}
	}
	return result, nil
}

func (r ScoutRunner) run(ctx context.Context, task ScoutTask, prompt string, lifecycle *Lifecycle) (ScoutResult, error) {
	messages := []client.Message{
		{Role: client.RoleSystem, Content: scoutInstructions()},
		{Role: client.RoleUser, Content: prompt},
	}
	tools := scoutTools(r.Tools.Tools())
	rounds := r.MaxRounds
	if rounds <= 0 {
		rounds = defaultScoutRounds
	}
	reminders := 0
	var usage client.Usage
	for round := 0; round < rounds; round++ {
		reportProgress(r.Progress, ProgressEvent{
			Agent: "scout", TaskID: task.ID, ParentID: task.ParentID,
			Action: ProgressThinking, Detail: fmt.Sprintf("model round %d", round+1),
		})
		parallel := false
		request := client.ChatRequest{
			Messages: messages, Tools: tools, ToolChoice: client.ToolChoiceAuto,
			ParallelToolCalls: &parallel, WorkingDirectory: r.WorkingDirectory,
		}
		if reminders > 0 {
			request.ToolChoice = client.NamedToolChoice(ScoutCompleteToolName)
		}
		r.Spec.Apply(&request)
		response, err := r.Client.Chat(ctx, request)
		if err != nil {
			return ScoutResult{}, fmt.Errorf("subagent: scout model: %w", err)
		}
		assistant, err := scoutAssistantMessage(response)
		if err != nil {
			return ScoutResult{}, err
		}
		usage = addUsage(usage, response.Usage)
		messages = append(messages, assistant)
		if lifecycle != nil {
			if err := lifecycle.Message(assistant); err != nil {
				return ScoutResult{}, err
			}
		}
		if len(assistant.ToolCalls) == 0 {
			if reminders == maximumScoutReminders {
				return ScoutResult{}, errors.New("subagent: scout ended without task_complete")
			}
			reminders++
			messages = append(messages, client.Message{Role: client.RoleSystem, Content: fmt.Sprintf(
				"Reminder %d/%d: finish the scout by calling task_complete now; do not answer with plain text.",
				reminders, maximumScoutReminders,
			)})
			continue
		}

		for _, call := range assistant.ToolCalls {
			reportProgress(r.Progress, ProgressEvent{
				Agent: "scout", TaskID: task.ID, ParentID: task.ParentID,
				Action: ProgressTool, Detail: call.Function.Name,
			})
			var toolResult client.ToolResult
			if call.Function.Name == ScoutCompleteToolName {
				if len(assistant.ToolCalls) != 1 {
					toolResult = scoutToolError(errors.New("task_complete must be the only tool call in its turn"))
				} else {
					completion, err := parseScoutCompletion(call.Function.Arguments)
					if err == nil {
						return ScoutResult{
							TaskID: task.ID, Status: sessionstore.StatusSucceeded,
							Outcome: completion.Outcome, Summary: completion.Summary,
							Findings: completion.Findings, Artifacts: completion.Artifacts,
							Verification: completion.Verification, Blocker: completion.Blocker, Usage: usage,
						}, nil
					}
					toolResult = scoutToolError(err)
				}
			} else if _, allowed := scoutReadTools[call.Function.Name]; !allowed {
				toolResult = scoutToolError(fmt.Errorf("tool %q is not available to scout", call.Function.Name))
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
			messages = append(messages, message)
			if lifecycle != nil {
				if err := lifecycle.Message(message); err != nil {
					return ScoutResult{}, err
				}
			}
		}
	}
	return ScoutResult{}, fmt.Errorf("subagent: scout exceeded %d model rounds", rounds)
}

func normalizeScoutTask(task ScoutTask) (ScoutTask, error) {
	task.ID = strings.TrimSpace(task.ID)
	task.ParentID = strings.TrimSpace(task.ParentID)
	task.Objective = strings.TrimSpace(task.Objective)
	if task.Objective == "" {
		return ScoutTask{}, errors.New("subagent: scout objective is required")
	}
	if task.ID == "" {
		id, err := sessionstore.NewID()
		if err != nil {
			return ScoutTask{}, err
		}
		task.ID = "scout-" + id
	}
	task.CompletionCriteria = cleanStrings(task.CompletionCriteria)
	task.CandidateFiles = cleanStrings(task.CandidateFiles)
	task.Context = cleanStrings(task.Context)
	seen := make(map[loom.Ref]struct{}, len(task.LoomInputs))
	refs := make([]loom.Ref, 0, len(task.LoomInputs))
	for _, ref := range task.LoomInputs {
		parsed, err := loom.ParseRef(ref.String())
		if err != nil {
			return ScoutTask{}, fmt.Errorf("subagent: scout Loom input: %w", err)
		}
		if _, exists := seen[parsed]; !exists {
			seen[parsed] = struct{}{}
			refs = append(refs, parsed)
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i] < refs[j] })
	task.LoomInputs = refs
	return task, nil
}

func cleanStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func encodeScoutTask(task ScoutTask) (string, error) {
	body, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return "", fmt.Errorf("subagent: encode scout task: %w", err)
	}
	return "Investigate this Griller-delegated repository question.\n\n" + string(body), nil
}

func scoutInstructions() string {
	return `You are q's isolated scout subagent. Investigate the repository and return precise evidence to the Griller that called you. The Griller may use your report to ask the user better questions, delegate another scout, or prepare a brief for the Planner. You cannot ask the user, modify files, run commands, create subagents, or declare the overall user task complete.

Rules:
1. Use only the supplied read-only repository and Loom tools.
2. Treat candidate_files and loom_inputs as optional leads, not as proof that no other file matters. When they are empty, discover the relevant area from the objective and available repository structure.
3. Prefer loom_eval when a large Loom artifact can be transformed into a smaller structured view before reading individual files.
4. Anchor findings to paths and symbols. Put concrete observations in evidence and keep inference in summary or risks.
5. Investigate enough surrounding code to identify existing patterns, dependencies, tests, and likely change boundaries.
6. If evidence is unavailable, return outcome blocked with a specific blocker; never invent repository facts.
7. Finish by calling task_complete as the only tool call in that turn. Never return the scout report as plain text.`
}

func scoutTools(available []client.Tool) []client.Tool {
	result := make([]client.Tool, 0, len(scoutReadTools)+1)
	for _, tool := range available {
		if _, allowed := scoutReadTools[tool.Function.Name]; allowed {
			result = append(result, tool)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Function.Name < result[j].Function.Name })
	result = append(result, scoutCompletionTool())
	return result
}

func scoutCompletionTool() client.Tool {
	strict := true
	stringArray := map[string]any{
		"type": "array", "maxItems": maximumScoutListItems,
		"items": map[string]any{"type": "string"},
	}
	return client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
		Name:        ScoutCompleteToolName,
		Description: "Finish this scout task with a structured repository report for the caller.",
		Strict:      &strict,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"outcome": map[string]any{"type": "string", "enum": []string{"succeeded", "blocked"}},
				"summary": map[string]any{"type": "string"},
				"findings": map[string]any{"type": "array", "maxItems": maximumScoutFindings, "items": map[string]any{
					"type": "object", "properties": map[string]any{
						"path": map[string]any{"type": "string"}, "symbol": map[string]any{"type": "string"},
						"summary": map[string]any{"type": "string"}, "evidence": stringArray, "risks": stringArray,
					}, "required": []string{"summary"}, "additionalProperties": false,
				}},
				"artifacts": stringArray, "verification": stringArray,
				"blocker": map[string]any{"type": "string"},
			},
			"required": []string{"outcome", "summary"}, "additionalProperties": false,
		},
	}}
}

func parseScoutCompletion(arguments string) (scoutCompletion, error) {
	var completion scoutCompletion
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&completion); err != nil {
		return scoutCompletion{}, fmt.Errorf("decode task_complete: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return scoutCompletion{}, errors.New("decode task_complete: multiple JSON values")
		}
		return scoutCompletion{}, fmt.Errorf("decode task_complete: %w", err)
	}
	completion.Outcome = strings.TrimSpace(completion.Outcome)
	completion.Summary = strings.TrimSpace(completion.Summary)
	completion.Blocker = strings.TrimSpace(completion.Blocker)
	if completion.Outcome != "succeeded" && completion.Outcome != "blocked" {
		return scoutCompletion{}, errors.New("task_complete outcome must be succeeded or blocked")
	}
	if completion.Summary == "" {
		return scoutCompletion{}, errors.New("task_complete summary is required")
	}
	if len(completion.Summary) > maximumScoutTextBytes || len(completion.Blocker) > maximumScoutTextBytes {
		return scoutCompletion{}, fmt.Errorf("task_complete summary and blocker must not exceed %d bytes", maximumScoutTextBytes)
	}
	if completion.Outcome == "blocked" && completion.Blocker == "" {
		return scoutCompletion{}, errors.New("task_complete blocker is required for blocked outcome")
	}
	if len(completion.Findings) > maximumScoutFindings {
		return scoutCompletion{}, fmt.Errorf("task_complete findings must contain at most %d items", maximumScoutFindings)
	}
	for index := range completion.Findings {
		finding := &completion.Findings[index]
		finding.Path = strings.TrimSpace(finding.Path)
		finding.Symbol = strings.TrimSpace(finding.Symbol)
		finding.Summary = strings.TrimSpace(finding.Summary)
		finding.Evidence = cleanStrings(finding.Evidence)
		finding.Risks = cleanStrings(finding.Risks)
		if finding.Summary == "" {
			return scoutCompletion{}, fmt.Errorf("task_complete finding %d requires summary", index+1)
		}
		if len(finding.Evidence) > maximumScoutListItems || len(finding.Risks) > maximumScoutListItems {
			return scoutCompletion{}, fmt.Errorf("task_complete finding %d evidence and risks must contain at most %d items", index+1, maximumScoutListItems)
		}
	}
	completion.Artifacts = cleanStrings(completion.Artifacts)
	completion.Verification = cleanStrings(completion.Verification)
	if len(completion.Artifacts) > maximumScoutListItems || len(completion.Verification) > maximumScoutListItems {
		return scoutCompletion{}, fmt.Errorf("task_complete artifacts and verification must contain at most %d items", maximumScoutListItems)
	}
	return completion, nil
}

func scoutAssistantMessage(response *client.ChatResponse) (client.Message, error) {
	if response == nil || len(response.Choices) == 0 {
		return client.Message{}, errors.New("subagent: scout model returned no choices")
	}
	message := response.Choices[0].Message
	if message.Role == "" {
		message.Role = client.RoleAssistant
	}
	return message, nil
}

func scoutToolError(err error) client.ToolResult {
	body, marshalErr := json.Marshal(map[string]string{"error": err.Error()})
	if marshalErr != nil {
		return client.ToolResult{Content: err.Error(), IsError: true}
	}
	return client.ToolResult{Content: string(body), IsError: true}
}

func addUsage(total, current client.Usage) client.Usage {
	total.PromptTokens += current.PromptTokens
	total.CompletionTokens += current.CompletionTokens
	total.TotalTokens += current.TotalTokens
	return total
}
