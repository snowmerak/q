package subagent

// These names belonged to Q's removed browser/search tools. Recognize only
// those names so old profiles remain usable without masking other missing tools.
// external_search and builtin/web-search are the supported ACP search path.
func retiredBuiltinSearchTool(name string) bool {
	switch name {
	case "browser_search", "web_search", "web_search_batch",
		"browser_open", "browser_read", "browser_snapshot", "browser_click", "browser_fill", "browser_close":
		return true
	}
	return false
}

func withoutRetiredSearchTools(names []string) []string {
	if names == nil {
		return nil
	}
	result := make([]string, 0, len(names))
	for _, name := range names {
		if !retiredBuiltinSearchTool(name) {
			result = append(result, name)
		}
	}
	return result
}
