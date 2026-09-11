package subagent

import "github.com/snowmerak/q/client"

func withRetrievalCatalog(prompt string, tools []client.Tool) string {
	if hasTool(tools, "search_skills") && hasTool(tools, "get_skill") {
		prompt += "\n\nAgent Skills are retrieved, not preloaded. Before starting substantive work, call search_skills with concise, task-specific keywords to identify applicable guidance, select a result, then call get_skill and follow the complete resource text returned directly in content. Before finalizing substantive work, search again using any new requirements, failures, or verification needs revealed by the work. Explicit $skill-name mentions should be searched by name."
	}
	if hasTool(tools, "search_archive") && hasTool(tools, "get_archive_record") {
		prompt += "\n\nWorkspace archive records are retrieved on demand. Before starting substantive work, call search_archive with concise, task-specific terms to check relevant prior conversations, decisions, agent results, and tool failures. Before finalizing substantive work, search again using any new decision terms, failures, or verification questions revealed by the work. Use get_archive_record only for selected results that need more detail."
	}
	if hasTool(tools, "search_propositions") && hasTool(tools, "get_proposition") {
		prompt += "\n\nDurable cross-workspace facts are stored as global propositions. Search them with search_propositions when prior preferences, decisions, constraints, or reusable resolutions may matter; call get_proposition only for a selected result that needs provenance or extraction details."
	}
	return prompt
}
