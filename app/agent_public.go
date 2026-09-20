package app

import (
	"fmt"
	"path/filepath"

	"github.com/snowmerak/q/agentinstructions"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
)

// AgentLoopRequest contains the host-owned dependencies and conversation state
// used by RunAgentLoop. RunAgentLoop copies Messages and does not close Client or
// Tools.
type AgentLoopRequest struct {
	Client               ChatClient
	Tools                AgentToolRuntime
	Model                string
	ReasoningEffort      string
	Messages             []client.Message
	ConversationID       string
	WorkingDirectory     string
	ActiveTask           *workspace.ActiveTask
	Stream               bool
	CoalesceInstructions bool
	ContextPolicy        memory.Policy
}

// AgentLoopResult is the terminal model response and loop accounting data.
type AgentLoopResult struct {
	Response        *client.ChatResponse
	Outcome         string
	RequestEstimate int
	ToolCalls       int
}

// WorkspaceMessageOptions controls the Q workspace instructions appended by
// PrepareWorkspaceMessages.
type WorkspaceMessageOptions struct {
	Root             string
	Tools            AgentToolRuntime
	ArchiveAvailable bool
}

// Status reports a transient loop status.
func (e AgentEvent) Status() (string, bool) {
	return e.status, e.status != ""
}

// Message reports a message appended to the loop transcript.
func (e AgentEvent) Message() (client.Message, bool) {
	if e.message == nil {
		return client.Message{}, false
	}
	return *e.message, true
}

// MessageIsToolError reports whether Message is a failed tool result.
func (e AgentEvent) MessageIsToolError() bool {
	return e.toolIsError
}

// ToolCall reports a tool call about to be handled by the loop.
func (e AgentEvent) ToolCall() (client.ToolCall, bool) {
	if e.call == nil {
		return client.ToolCall{}, false
	}
	return *e.call, true
}

// Question reports an interactive question and the channel used to resume the
// loop. The host must send exactly one AgentAnswer when it accepts the question.
func (e AgentEvent) Question() (AgentQuestion, chan<- AgentAnswer, bool) {
	if e.question == nil || e.answer == nil {
		return AgentQuestion{}, nil, false
	}
	return *e.question, e.answer, true
}

// Compaction reports a context checkpoint for optional host persistence.
func (e AgentEvent) Compaction() (AgentContextCompaction, bool) {
	if e.compaction == nil {
		return AgentContextCompaction{}, false
	}
	return *e.compaction, true
}

// ContextReplacement reports a repaired message that the host should use when
// it persists the transcript.
func (e AgentEvent) ContextReplacement() (AgentContextReplacement, bool) {
	if e.contextReplace == nil {
		return AgentContextReplacement{}, false
	}
	return *e.contextReplace, true
}

// StreamDelta reports a partial thinking or response fragment.
func (e AgentEvent) StreamDelta() (AgentStreamDelta, bool) {
	if e.streamDelta == nil {
		return AgentStreamDelta{}, false
	}
	return *e.streamDelta, true
}

// TaskStarted reports the task lifecycle opened by task_start.
func (e AgentEvent) TaskStarted() (workspace.ActiveTask, bool) {
	if e.taskStarted == nil {
		return workspace.ActiveTask{}, false
	}
	return *e.taskStarted, true
}

// TaskCompleted reports a successful task_complete call.
func (e AgentEvent) TaskCompleted() bool {
	return e.taskCompleted
}

// Result reports the terminal loop result. A terminal result may contain a nil
// response when the model returned no choices.
func (e AgentEvent) Result() (AgentLoopResult, bool) {
	if !e.complete {
		return AgentLoopResult{}, false
	}
	return AgentLoopResult{
		Response:        e.response,
		Outcome:         e.outcome,
		RequestEstimate: e.requestEstimate,
		ToolCalls:       e.toolCalls,
	}, true
}

// Err reports a terminal loop error.
func (e AgentEvent) Err() error {
	return e.err
}

// PrepareWorkspaceMessages appends Q's existing workspace, tool-discovery, and
// orchestration instructions without mutating messages.
func PrepareWorkspaceMessages(messages []client.Message, options WorkspaceMessageOptions) []client.Message {
	result := append([]client.Message(nil), messages...)
	if options.Root != "" {
		loader := agentinstructions.New(options.Root, result)
		result = append(result, loader.Root()...)
	}
	if options.Tools != nil && options.Root != "" {
		environment := options.Tools.Environment()
		workspacePrompt := fmt.Sprintf(
			"Runtime environment: OS=%s; architecture=%s; run_command shell=%s. Use commands and quoting compatible with this shell. ",
			environment.OS, environment.Architecture, environment.Shell,
		) + "Current workspace root: " + filepath.Clean(options.Root) +
			". Use the available tools to inspect, edit, and run work in this workspace when the user asks for changes." +
			" For repository discovery, never traverse q's .q metadata directory and honor patterns in the workspace-root .qignore file, including when scanning through run_command. Explicit ignored-path access is allowed when the task requires it." +
			" Non-Loom MCP tool results include a loom_ref to the immutable full result. For large results, use loom_inspect, loom_read, or loom_eval instead of copying the result through chat context."
		if options.ArchiveAvailable {
			workspacePrompt += " Before starting substantive work that requires tools or multiple steps, call search_archive with concise, task-specific terms to check relevant prior workspace conversations, decisions, agent results, and tool failures. Before finalizing substantive work, search again using any new decision terms, failures, or verification questions revealed by the work. Use get_archive_record only for selected results that need more detail."
		}
		result = append(result, client.Message{
			Role: client.RoleDeveloper, Name: "q_workspace",
			Content: workspacePrompt,
		})
	}
	if options.Tools == nil {
		return result
	}
	toolsAvailable := true
	if runtime, ok := options.Tools.(*qtools.Runtime); ok && runtime == nil {
		toolsAvailable = false
	}
	var runtimeTools []client.Tool
	if toolsAvailable {
		runtimeTools = options.Tools.Tools()
	}
	for _, tool := range runtimeTools {
		if tool.Function.Name == "search_skills" {
			result = append(result, client.Message{
				Role: client.RoleDeveloper, Name: "q_agent_skills",
				Content: "Agent Skills are retrieved on demand from the global q Library and the workspace skill index rather than preloaded. At the start of work, or after receiving new information, call search_skills with concise, task-specific keywords when additional guidance is needed to perform the work or handle that information. Select a relevant result, then call get_skill and follow the complete resource text returned directly in content. Search explicit $skill-name mentions by name.",
			})
			break
		}
	}
	for _, tool := range runtimeTools {
		if tool.Function.Name == "search_propositions" {
			result = append(result, client.Message{
				Role: client.RoleDeveloper, Name: "q_propositions",
				Content: "Durable cross-workspace facts are stored as global q Library propositions. When a prior preference, decision, constraint, stable fact, or reusable resolution may be relevant, call search_propositions, then call get_proposition only for a selected result that needs full provenance or extraction metadata.",
			})
			break
		}
	}
	result = append(result, client.Message{
		Role: client.RoleDeveloper, Name: "q_orchestration",
		Content: "Use task_start before work that requires tools or multiple execution steps. Once task_start succeeds, that task must finish with exactly one successful task_complete call; do not finish it with a plain assistant response. " +
			"Direct questions and short answers may finish normally without task_start or task_complete. Use ask_to_user when a required user decision or missing detail prevents safe progress; wait for the answer and then continue the same turn. " +
			"Call task_complete only for a started task after all requested work and appropriate verification are done, or with outcome blocked when progress genuinely cannot continue.",
	})
	return result
}
