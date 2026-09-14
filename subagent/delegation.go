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
	"github.com/snowmerak/q/sessionstore"
)

const (
	DelegateListToolName = "delegate_list"
	DelegateToolName     = "delegate"
	TaskStartToolName    = "task_start"
	TaskCompleteToolName = "task_complete"

	BuiltinScoutID    = "builtin/scout"
	BuiltinGrillerID  = "builtin/griller"
	BuiltinPlannerID  = "builtin/planner"
	BuiltinReviewerID = "builtin/reviewer"
	BuiltinCoderID    = "builtin/coder"
	// MaximumDelegatePromptBytes bounds one explicit parent-to-child request.
	MaximumDelegatePromptBytes = 32 << 10

	defaultGeneralRounds    = 320
	maximumGeneralReminders = 3
)

type DelegateInfo struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	Source           string `json:"source"`
	Role             string `json:"role"`
	MutatesWorkspace bool   `json:"mutates_workspace"`
}

type AgentDefinition struct {
	Info         DelegateInfo
	SystemPrompt string
	Tools        []string
	Delegates    []string
	StrictTools  bool
}

type Registry struct {
	definitions map[string]AgentDefinition
}

func NewRegistry(definitions []AgentDefinition) (*Registry, error) {
	registry := &Registry{definitions: make(map[string]AgentDefinition, len(definitions))}
	for _, definition := range definitions {
		definition.Info.Name = strings.TrimSpace(definition.Info.Name)
		definition.Info.Description = strings.TrimSpace(definition.Info.Description)
		definition.Info.Source = strings.TrimSpace(definition.Info.Source)
		definition.Info.Role = strings.TrimSpace(definition.Info.Role)
		definition.SystemPrompt = strings.TrimSpace(definition.SystemPrompt)
		if !ValidAgentID(definition.Info.Name) {
			return nil, fmt.Errorf("subagent: invalid agent ID %q", definition.Info.Name)
		}
		if definition.Info.Role == "" || definition.SystemPrompt == "" {
			return nil, fmt.Errorf("subagent: agent %q requires role and system prompt", definition.Info.Name)
		}
		if _, exists := registry.definitions[definition.Info.Name]; exists {
			return nil, fmt.Errorf("subagent: duplicate agent ID %q", definition.Info.Name)
		}
		definition.Tools = cleanUniqueNames(definition.Tools)
		definition.Delegates = cleanUniqueNames(definition.Delegates)
		registry.definitions[definition.Info.Name] = definition
	}
	for name, definition := range registry.definitions {
		for _, target := range definition.Delegates {
			if target == name {
				return nil, fmt.Errorf("subagent: agent %q cannot delegate to itself", name)
			}
			if _, exists := registry.definitions[target]; !exists {
				return nil, fmt.Errorf("subagent: agent %q references unknown delegate %q", name, target)
			}
			if strings.HasPrefix(name, "global/") && strings.HasPrefix(target, "workspace/") {
				return nil, fmt.Errorf("subagent: global agent %q cannot delegate to workspace agent %q", name, target)
			}
		}
	}
	visiting := make(map[string]bool, len(registry.definitions))
	visited := make(map[string]bool, len(registry.definitions))
	var visit func(string, []string) error
	visit = func(name string, path []string) error {
		if visiting[name] {
			return fmt.Errorf("subagent: delegation cycle: %s", strings.Join(append(path, name), " -> "))
		}
		if visited[name] {
			return nil
		}
		visiting[name] = true
		for _, target := range registry.definitions[name].Delegates {
			if err := visit(target, append(path, name)); err != nil {
				return err
			}
		}
		delete(visiting, name)
		visited[name] = true
		return nil
	}
	for name := range registry.definitions {
		if err := visit(name, nil); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (r *Registry) Get(name string) (AgentDefinition, bool) {
	if r == nil {
		return AgentDefinition{}, false
	}
	definition, found := r.definitions[name]
	return definition, found
}

func (r *Registry) List() []DelegateInfo {
	if r == nil {
		return nil
	}
	result := make([]DelegateInfo, 0, len(r.definitions))
	for _, definition := range r.definitions {
		result = append(result, definition.Info)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (r *Registry) Allowed(caller string) []DelegateInfo {
	if r == nil {
		return nil
	}
	var names []string
	if caller == "" {
		for name := range r.definitions {
			names = append(names, name)
		}
	} else if definition, found := r.definitions[caller]; found {
		names = append(names, definition.Delegates...)
	}
	sort.Strings(names)
	result := make([]DelegateInfo, 0, len(names))
	for _, name := range names {
		if definition, found := r.definitions[name]; found {
			result = append(result, definition.Info)
		}
	}
	return result
}

func (r *Registry) CanDelegate(caller, target string) bool {
	for _, info := range r.Allowed(caller) {
		if info.Name == target {
			return true
		}
	}
	return false
}

func ValidAgentID(name string) bool {
	scope, value, found := strings.Cut(strings.TrimSpace(name), "/")
	if !found || (scope != "builtin" && scope != "global" && scope != "workspace") {
		return false
	}
	return config.ValidCustomName(value)
}

func CanonicalProfileID(scope, name string) string {
	if scope != "global" && scope != "workspace" {
		return ""
	}
	if !config.ValidCustomName(name) {
		return ""
	}
	return scope + "/" + name
}

func BuiltinAgentDefinitions() []AgentDefinition {
	readTools := []string{
		"read_file", "list_directory", "loom_inspect", "loom_read", "loom_eval",
		"search_skills", "get_skill", "search_propositions", "get_proposition",
		"search_archive", "get_archive_record", "lsp_status", "lsp_diagnostics",
		"lsp_hover", "lsp_definition", "lsp_references", "lsp_document_symbols",
		"lsp_workspace_symbols",
	}
	return []AgentDefinition{
		{
			Info: DelegateInfo{Name: BuiltinScoutID, Source: "builtin", Role: config.AgentRoleScout,
				Description: "Investigate repository evidence and report bounded findings."},
			SystemPrompt: "Investigate the explicit request using repository evidence. Do not modify the workspace. Distinguish observations from inference, cite relevant paths or symbols in findings, and report a concrete blocker when evidence is unavailable.",
			Tools:        readTools,
		},
		{
			Info: DelegateInfo{Name: BuiltinGrillerID, Source: "builtin", Role: config.AgentRoleGriller,
				Description: "Find ambiguity, missing constraints, assumptions, and risks in a request."},
			SystemPrompt: "Interrogate the explicit request for ambiguity, missing constraints, unsafe assumptions, and acceptance gaps. Do not modify the workspace and do not ask the user directly. Delegate repository questions when useful, then return the questions or constraints the caller should resolve.",
			Tools:        readTools, Delegates: []string{BuiltinScoutID},
		},
		{
			Info: DelegateInfo{Name: BuiltinPlannerID, Source: "builtin", Role: config.AgentRolePlanner,
				Description: "Turn a bounded request into an actionable implementation approach."},
			SystemPrompt: "Produce an actionable approach for the explicit request. Ground repository claims in evidence, keep scope bounded, include verification, and do not modify the workspace. This is advice for the caller, not an approved q /plan proposal.",
			Tools:        readTools, Delegates: []string{BuiltinScoutID},
		},
		{
			Info: DelegateInfo{Name: BuiltinReviewerID, Source: "builtin", Role: config.AgentRoleAdvisor,
				Description: "Review requested code or results without modifying the workspace."},
			SystemPrompt: "Review only the code, changes, or result named in the explicit request. Do not modify the workspace. Prioritize concrete correctness, security, data-loss, concurrency, and regression risks; avoid style-only comments and identify evidence locations.",
			Tools:        readTools, Delegates: []string{BuiltinScoutID},
		},
		{
			Info: DelegateInfo{Name: BuiltinCoderID, Source: "builtin", Role: config.AgentRoleCoder,
				Description: "Implement a bounded request in the workspace and verify it.", MutatesWorkspace: true},
			SystemPrompt: "Implement only the explicit bounded request. Inspect before editing, preserve unrelated changes, use the smallest coherent changes, and perform proportionate verification. Report exactly what changed and what verification ran.",
			Tools:        append(append([]string(nil), readTools...), "edit_file", "write_file", "create_directory", "move_path", "copy_path", "remove_path", "run_command", "wait"),
			Delegates:    []string{BuiltinScoutID, BuiltinReviewerID},
		},
	}
}

func DefinitionForProfile(entry ProfileEntry) (AgentDefinition, error) {
	if entry.Err != nil {
		return AgentDefinition{}, entry.Err
	}
	id := CanonicalProfileID(entry.Scope, entry.Profile.Name)
	if id == "" {
		return AgentDefinition{}, fmt.Errorf("subagent: invalid profile identity %s/%s", entry.Scope, entry.Profile.Name)
	}
	return AgentDefinition{
		Info: DelegateInfo{
			Name: id, Description: entry.Profile.Description, Source: entry.Scope, Role: entry.Profile.Role,
			MutatesWorkspace: profileMayMutate(entry.Profile.Tools),
		},
		SystemPrompt: entry.Profile.SystemPrompt,
		Tools:        append([]string(nil), entry.Profile.Tools...), Delegates: append([]string(nil), entry.Profile.Delegates...),
		StrictTools: true,
	}, nil
}

func profileMayMutate(tools []string) bool {
	for _, name := range tools {
		switch name {
		case "edit_file", "write_file", "create_directory", "move_path", "copy_path", "remove_path", "run_command":
			return true
		}
	}
	return false
}

func cleanUniqueNames(values []string) []string {
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
	sort.Strings(result)
	return result
}

type taskStartInput struct {
	Objective          string   `json:"objective"`
	CompletionCriteria []string `json:"completion_criteria,omitempty"`
}

type taskCompleteInput struct {
	Outcome      string   `json:"outcome"`
	Summary      string   `json:"summary"`
	Findings     []string `json:"findings,omitempty"`
	Artifacts    []string `json:"artifacts,omitempty"`
	Verification []string `json:"verification,omitempty"`
	Blocker      string   `json:"blocker,omitempty"`
}

func TaskLifecycleTools() []client.Tool {
	strict := true
	textSchema := map[string]any{"type": "string", "maxLength": maximumCoderTextBytes}
	stringsSchema := map[string]any{
		"type": "array", "maxItems": maximumCoderListItems,
		"items": map[string]any{"type": "string", "maxLength": maximumCoderTextBytes},
	}
	return []client.Tool{
		{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
			Name: TaskStartToolName, Description: "Start this delegated task before using tools. It must later finish with task_complete.", Strict: &strict,
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"objective": textSchema, "completion_criteria": stringsSchema,
			}, "required": []string{"objective"}, "additionalProperties": false},
		}},
		{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
			Name: TaskCompleteToolName, Description: "Finish the active delegated task with its result.", Strict: &strict,
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"outcome": map[string]any{"type": "string", "enum": []string{"succeeded", "blocked"}},
				"summary": textSchema, "findings": stringsSchema,
				"artifacts": stringsSchema, "verification": stringsSchema,
				"blocker": textSchema,
			}, "required": []string{"outcome", "summary"}, "additionalProperties": false},
		}},
	}
}

func DelegateTools() []client.Tool {
	strict := true
	return []client.Tool{
		{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
			Name: DelegateListToolName, Description: "List the subagents this agent is currently allowed to call.", Strict: &strict,
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		}},
		{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
			Name: DelegateToolName, Description: "Run one allowed subagent synchronously for a bounded prompt.", Strict: &strict,
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"subagent_name": map[string]any{"type": "string"},
				"prompt":        map[string]any{"type": "string", "maxLength": MaximumDelegatePromptBytes},
			}, "required": []string{"subagent_name", "prompt"}, "additionalProperties": false},
		}},
	}
}

type GeneralRunner struct {
	Client                               AgentClient
	Tools                                ToolRuntime
	Spec                                 Spec
	Definition                           AgentDefinition
	WorkingDirectory, Environment, RunID string
	ParentID                             string
	Sink                                 RecordSink
	Progress                             ProgressFunc
	Trace                                TraceFunc
	MaxRounds                            int
}

func (r GeneralRunner) Run(ctx context.Context, prompt string) (result TaskResult, runErr error) {
	if ctx == nil || r.Client == nil {
		return TaskResult{}, errors.New("subagent: general runner requires context and client")
	}
	if r.Tools == nil {
		r.Tools = emptyToolRuntime{}
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return TaskResult{}, errors.New("subagent: request is required")
	}
	if len(prompt) > MaximumDelegatePromptBytes {
		return TaskResult{}, fmt.Errorf("subagent: request must not exceed %d bytes", MaximumDelegatePromptBytes)
	}
	if r.Spec.Role != r.Definition.Info.Role {
		return TaskResult{}, errors.New("subagent: definition role does not match resolved model")
	}
	available, err := selectDefinitionTools(r.Definition, r.Tools)
	if err != nil {
		return TaskResult{}, err
	}
	taskID, err := sessionstore.NewID()
	if err != nil {
		return TaskResult{}, err
	}
	taskID = strings.ReplaceAll(r.Definition.Info.Name, "/", "-") + "-" + taskID
	progress := func(action, detail string) {
		reportProgress(r.Progress, ProgressEvent{Agent: r.Definition.Info.Name, TaskID: taskID, ParentID: r.ParentID, Action: action, Detail: detail})
	}
	progress(ProgressStarted, prompt)
	var lifecycle *Lifecycle
	if r.Sink != nil {
		lifecycle, err = NewLifecycle(r.Sink, r.RunID, taskID, r.ParentID, &r.Spec)
		if err != nil {
			return TaskResult{}, err
		}
		snapshot, _ := json.Marshal(map[string]any{"agent": r.Definition.Info, "request": prompt, "model": r.Spec.Model, "candidates": r.Spec.Candidates})
		if err = lifecycle.Queued(string(snapshot)); err != nil {
			return TaskResult{}, err
		}
		if err = lifecycle.Started(prompt); err != nil {
			return TaskResult{}, err
		}
	}
	defer func() {
		if runErr != nil {
			progress(ProgressFailed, runErr.Error())
			if lifecycle != nil {
				if errors.Is(runErr, context.Canceled) {
					runErr = errors.Join(runErr, lifecycle.Cancelled("Canceled"))
				} else {
					runErr = errors.Join(runErr, lifecycle.Failed(runErr))
				}
			}
			return
		}
		if lifecycle != nil {
			body, _ := json.Marshal(result)
			runErr = lifecycle.Succeeded(result.Summary, string(body), result)
		}
		if runErr != nil {
			progress(ProgressFailed, runErr.Error())
		} else {
			progress(ProgressCompleted, result.Summary)
		}
	}()

	systemPrompt := r.Definition.SystemPrompt + "\n\nRuntime environment: " + r.Environment + "\nWorking directory: " + r.WorkingDirectory +
		"\n\nStart by calling task_start. Do not call any other tool before task_start succeeds. Finish with task_complete as the only tool call in that turn. task_complete uses the common schema exactly; do not invent agent-specific fields."
	messages := []client.Message{{Role: client.RoleSystem, Content: systemPrompt}, {Role: client.RoleUser, Content: prompt}}
	history := NewContextCompactor(r.Spec, messages, available, len(messages))
	rounds := r.MaxRounds
	if rounds <= 0 {
		rounds = defaultGeneralRounds
	}
	started, reminders := false, 0
	for round := 0; round < rounds; round++ {
		if err = ctx.Err(); err != nil {
			return TaskResult{}, err
		}
		if err = history.CompactIfNeeded(ctx, &r.Spec, r.Client); err != nil {
			return TaskResult{}, fmt.Errorf("subagent: context: %w", err)
		}
		progress(ProgressThinking, fmt.Sprintf("model round %d", round+1))
		parallel := false
		request := client.ChatRequest{Messages: history.RequestMessages(), Tools: available, ToolChoice: client.ToolChoiceAuto, ParallelToolCalls: &parallel, WorkingDirectory: r.WorkingDirectory}
		response, err := r.Spec.Chat(ctx, r.Client, request)
		if err != nil {
			return TaskResult{}, fmt.Errorf("subagent: model: %w", err)
		}
		assistant, err := scoutAssistantMessage(response)
		if err != nil {
			return TaskResult{}, err
		}
		history.Observe(response.Usage)
		history.Append(assistant)
		traceAssistant(r.Trace, r.Definition.Info.Name, taskID, r.ParentID, assistant)
		if lifecycle != nil {
			if err = lifecycle.Message(assistant); err != nil {
				return TaskResult{}, err
			}
		}
		if len(assistant.ToolCalls) == 0 {
			if reminders >= maximumGeneralReminders {
				return TaskResult{}, errors.New("subagent: agent ended without task_complete")
			}
			reminders++
			instruction := "Call task_start before doing work."
			if started {
				instruction = "Continue the work if needed, then call task_complete."
			}
			history.Append(client.Message{Role: client.RoleSystem, Content: instruction})
			continue
		}
		for _, call := range assistant.ToolCalls {
			progress(ProgressTool, call.Function.Name)
			var toolResult client.ToolResult
			switch call.Function.Name {
			case TaskStartToolName:
				input, parseErr := parseGeneralTaskStart(call.Function.Arguments)
				if parseErr == nil && started {
					parseErr = errors.New("another task_start lifecycle is already active")
				}
				if parseErr != nil {
					toolResult = scoutToolError(parseErr)
				} else {
					started = true
					body, _ := json.Marshal(map[string]any{"started": true, "objective": input.Objective})
					toolResult = client.ToolResult{Content: string(body)}
				}
			case TaskCompleteToolName:
				if !started {
					toolResult = scoutToolError(errors.New("task_complete requires task_start"))
				} else if len(assistant.ToolCalls) != 1 {
					toolResult = scoutToolError(errors.New("task_complete must be the only tool call in its turn"))
				} else {
					completed, parseErr := parseGeneralTaskComplete(call.Function.Arguments)
					if parseErr != nil {
						toolResult = scoutToolError(parseErr)
					} else {
						body, _ := json.Marshal(completed)
						_, finishErr := finishRoleTool(ctx, r.Client, &r.Spec, history, request, call, client.ToolResult{Content: string(body)}, r.Trace, r.Definition.Info.Name, taskID, r.ParentID, lifecycle)
						if finishErr != nil {
							return TaskResult{}, finishErr
						}
						return completed, nil
					}
				}
			default:
				if !started {
					toolResult = scoutToolError(errors.New("call task_start before using tools"))
				} else if !hasTool(available, call.Function.Name) {
					toolResult = scoutToolError(fmt.Errorf("tool %q is unavailable", call.Function.Name))
				} else {
					toolResult, err = r.Tools.Call(ctx, call)
					if err != nil {
						if ctx.Err() != nil {
							return TaskResult{}, ctx.Err()
						}
						toolResult = scoutToolError(err)
					}
				}
			}
			traceToolResult(r.Trace, r.Definition.Info.Name, taskID, r.ParentID, call, toolResult)
			message := client.ToolResultMessage(call, toolResult)
			history.Append(message)
			if lifecycle != nil {
				if err = lifecycle.Message(message); err != nil {
					return TaskResult{}, err
				}
			}
		}
	}
	return TaskResult{}, fmt.Errorf("subagent: agent exceeded %d model rounds", rounds)
}

type emptyToolRuntime struct{}

func (emptyToolRuntime) Tools() []client.Tool { return nil }
func (emptyToolRuntime) Call(context.Context, client.ToolCall) (client.ToolResult, error) {
	return client.ToolResult{}, errors.New("subagent: tool runtime is unavailable")
}

func selectDefinitionTools(definition AgentDefinition, runtime ToolRuntime) ([]client.Tool, error) {
	wanted := make(map[string]struct{}, len(definition.Tools))
	for _, name := range definition.Tools {
		wanted[name] = struct{}{}
	}
	var result []client.Tool
	found := make(map[string]bool, len(wanted))
	for _, tool := range runtime.Tools() {
		name := tool.Function.Name
		if _, selected := wanted[name]; selected || ((name == DelegateListToolName || name == DelegateToolName) && len(definition.Delegates) > 0) {
			result = append(result, tool)
			found[name] = true
		}
	}
	if definition.StrictTools {
		for name := range wanted {
			if !found[name] {
				return nil, fmt.Errorf("subagent %s: unavailable tool %q", definition.Info.Name, name)
			}
		}
	}
	result = append(result, TaskLifecycleTools()...)
	sort.Slice(result, func(i, j int) bool { return result[i].Function.Name < result[j].Function.Name })
	return result, nil
}

func parseGeneralTaskStart(arguments string) (taskStartInput, error) {
	var input taskStartInput
	if err := decodeGeneralArguments(arguments, &input); err != nil {
		return taskStartInput{}, err
	}
	input.Objective = strings.TrimSpace(input.Objective)
	if input.Objective == "" {
		return taskStartInput{}, errors.New("task_start objective is required")
	}
	input.CompletionCriteria = cleanStrings(input.CompletionCriteria)
	if len(input.Objective) > maximumCoderTextBytes || !boundedAgentStrings(input.CompletionCriteria) {
		return taskStartInput{}, fmt.Errorf("task_start text must not exceed %d bytes and criteria must contain at most %d items", maximumCoderTextBytes, maximumCoderListItems)
	}
	return input, nil
}

func parseGeneralTaskComplete(arguments string) (TaskResult, error) {
	var input taskCompleteInput
	if err := decodeGeneralArguments(arguments, &input); err != nil {
		return TaskResult{}, fmt.Errorf("decode task_complete: %w", err)
	}
	result := TaskResult{
		Outcome: input.Outcome, Summary: input.Summary, Findings: input.Findings,
		Artifacts: input.Artifacts, Verification: input.Verification, Blocker: input.Blocker,
	}
	result.Outcome = strings.TrimSpace(result.Outcome)
	result.Summary = strings.TrimSpace(result.Summary)
	result.Blocker = strings.TrimSpace(result.Blocker)
	result.Findings = cleanStrings(result.Findings)
	result.Artifacts = cleanStrings(result.Artifacts)
	result.Verification = cleanStrings(result.Verification)
	if result.Outcome != "blocked" && result.Blocker != "" {
		return TaskResult{}, errors.New("task_complete blocker is valid only for blocked outcome")
	}
	if err := validateCoderResult(result); err != nil {
		return TaskResult{}, err
	}
	return result, nil
}

func decodeGeneralArguments(arguments string, output any) error {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
