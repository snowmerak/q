package subagent

func withSkillCatalog(prompt string, tools ToolRuntime) string {
	if hasSkillRetrieval(tools) {
		prompt += "\n\nAgent Skills are retrieved, not preloaded. When reusable procedural guidance may help, call search_skills with concise keywords, select a result, then call get_skill and inspect its Loom artifact before following it. Explicit $skill-name mentions should be searched by name."
	}
	if hasPropositionRetrieval(tools) {
		prompt += "\n\nDurable cross-workspace facts are stored as global propositions. Search them with search_propositions when prior preferences, decisions, constraints, or reusable resolutions may matter; call get_proposition only for a selected result that needs provenance or extraction details."
	}
	return prompt
}

func hasPropositionRetrieval(tools ToolRuntime) bool {
	if tools == nil {
		return false
	}
	for _, tool := range tools.Tools() {
		if tool.Function.Name == "search_propositions" {
			return true
		}
	}
	return false
}

func hasSkillRetrieval(tools ToolRuntime) bool {
	if tools == nil {
		return false
	}
	for _, tool := range tools.Tools() {
		if tool.Function.Name == "search_skills" {
			return true
		}
	}
	return false

}
