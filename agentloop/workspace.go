package agentloop

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/agentinstructions"
	"github.com/snowmerak/q/client"
	qtools "github.com/snowmerak/q/tools"
)

// PrepareWorkspaceMessages appends Q's existing workspace, tool-discovery, and
// orchestration instructions without mutating messages.
func PrepareWorkspaceMessages(messages []client.Message, options WorkspaceMessageOptions) []client.Message {
	result := append([]client.Message(nil), messages...)
	if options.Root != "" {
		loader := agentinstructions.NewRootSet(options.Root, options.AuxiliaryRoots, result)
		result = append(result, loader.Root()...)
	}
	if options.Tools != nil && options.Root != "" {
		environment := options.Tools.Environment()
		workspacePrompt := fmt.Sprintf(
			"Runtime environment: OS=%s; architecture=%s; run_command shell=%s. Use commands and quoting compatible with this shell. ",
			environment.OS, environment.Architecture, environment.Shell,
		) + "Primary workspace root: " + filepath.Clean(options.Root) + "."
		if strings.TrimSpace(options.ProjectName) != "" {
			workspacePrompt += " Studio project: " + strings.TrimSpace(options.ProjectName) + "."
		}
		if len(options.AuxiliaryRoots) > 0 {
			cleaned := make([]string, 0, len(options.AuxiliaryRoots))
			for _, root := range options.AuxiliaryRoots {
				if strings.TrimSpace(root) != "" {
					cleaned = append(cleaned, filepath.Clean(root))
				}
			}
			if len(cleaned) > 0 {
				workspacePrompt += " Auxiliary workspace roots: " + strings.Join(cleaned, "; ") + ". The primary workspace owns this session. Auxiliary workspaces are additional execution locations; use their absolute paths for direct file or LSP tools and as run_command workdir or delegation working_directory when work belongs there. Delegated sessions remain children of this primary session."
			}
		}
		workspacePrompt += " Use the available tools to inspect, edit, and run work in these configured workspaces when the user asks for changes." +
			" For repository discovery, never traverse q's .q metadata directory and honor patterns in the corresponding workspace-root .qignore file, including when scanning through run_command. Explicit ignored-path access is allowed when the task requires it." +
			" Non-Loom MCP tool results include a loom_ref to the immutable full result. For large results, use loom_inspect, loom_read, or loom_eval instead of copying the result through chat context."
		if options.AllowedTools != nil {
			workspacePrompt = "Session root: " + filepath.Clean(options.Root) + ". Use only tools in the authorized catalog. Available artifact tools operate on their documented paths; the session root does not grant general filesystem or command access."
		}
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
	runtimeTools = filterTools(runtimeTools, options.AllowedTools)
	for _, tool := range runtimeTools {
		if tool.Function.Name == "search_skills" {
			result = append(result, client.Message{
				Role: client.RoleDeveloper, Name: "q_agent_skills",
				Content: "Agent Skills are retrieved on demand from the global q Library and the workspace skill index rather than preloaded. Before substantive work, call search_skills to find instructions suited to the current work environment, the project's nature, and the user request. Inspect the workspace just enough to choose useful search terms if its nature is not yet clear. Call get_skill for each applicable result and follow the complete resource text returned directly in content. After receiving new information, search again when additional guidance is needed. Search explicit $skill-name mentions by name.",
			})
			break
		}
	}
	for _, tool := range runtimeTools {
		if tool.Function.Name == "search_work" {
			result = append(result, client.Message{
				Role: client.RoleDeveloper, Name: "q_workspace_knowledge",
				Content: "Workspace issues and ticket threads contain project knowledge. Use search_work for relevant evidence, decisions and constraints; use issue_get/ticket_get for threads and post_get for complete versioned bodies or replies. Search stays in this workspace unless all:true is explicitly requested. Record verified lessons and decisions with evidence in issue_create or issue_reply. Source storage, indexing, ticket execution and review completion are distinct acknowledgements.",
			})
			break
		}
	}
	orchestrationPrompt := "Use task_start before work that requires tools or multiple execution steps. Once task_start succeeds, that task must finish with exactly one successful task_complete call; do not finish it with a plain assistant response. " +
		"Direct questions and short answers may finish normally without task_start or task_complete. Use ask_to_user when a required user decision or missing detail prevents safe progress; wait for the answer and then continue the same turn. " +
		"Call task_complete only for a started task after all requested work and appropriate verification are done, or with outcome blocked when progress genuinely cannot continue."
	loopTools := filterTools(orchestrationTools(), options.AllowedTools)
	if !catalogHas(loopTools, taskStartToolName) || !catalogHas(loopTools, taskCompleteToolName) {
		orchestrationPrompt = ""
		if catalogHas(loopTools, askToUserToolName) {
			orchestrationPrompt = "Use ask_to_user to clarify a required user decision or missing detail. Wait for the user's answer, then continue the same conversation. Respond directly when no question or tool is needed."
		}
	}
	if catalogHas(runtimeTools, "ticket_create") {
		orchestrationPrompt += " To assign work to a subagent, inspect workspace_members and issue ticket_create in the current project. Ticket creation acknowledges durable queuing; System One selects a joined subagent and execution and Integrator review complete asynchronously. Report the ticket IDs and do not call delegate_list or delegate or synchronously wait for another worker. If no project is bound, ask the user to select one before issuing tickets."
	}
	if orchestrationPrompt != "" {
		result = append(result, client.Message{
			Role: client.RoleDeveloper, Name: "q_orchestration",
			Content: orchestrationPrompt,
		})
	}
	return result
}
