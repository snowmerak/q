package agentloop

import (
	"fmt"
	"path/filepath"

	"github.com/snowmerak/q/agentinstructions"
	"github.com/snowmerak/q/client"
	qtools "github.com/snowmerak/q/tools"
)

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
