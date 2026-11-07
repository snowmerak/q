package app

import "strings"

// canResumeSessionConversation keeps provider-owned thread IDs separate from
// Responses prompt-cache affinity. Use the configured route, not its prefix's
// spelling, so custom Codex prefixes work and ordinary API IDs stay transient.
func (m model) canResumeSessionConversation(key string) bool {
	if key == "" {
		return false
	}
	if strings.HasPrefix(key, "cache_") {
		return m.activeModelAPIMode(m.activeModel()) == "responses"
	}
	index, _, found := gatewayModelLocation(m.gatewayConfig, m.activeModel())
	if !found || !m.gatewayConfig.Providers[index].Enabled {
		return false
	}
	kind := m.gatewayConfig.Providers[index].Type
	return kind == "codex" || kind == "codex-app-server"
}
