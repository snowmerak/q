package subagent

func withSkillCatalog(prompt string, tools ToolRuntime) string {
	if !hasSkillRetrieval(tools) {
		return prompt
	}
	return prompt + "\n\nAgent Skills are retrieved, not preloaded. When reusable procedural guidance may help, call search_skills with concise keywords, select a result, then call get_skill and inspect its Loom artifact before following it. Explicit $skill-name mentions should be searched by name."
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
