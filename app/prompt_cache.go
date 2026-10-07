package app

import "github.com/snowmerak/llm-provider/gateway"

// anthropicRequestExtra keeps the Council cache choice scoped to the selected
// native Anthropic route. Other sessions retain the Gateway's default policy.
func (m model) anthropicRequestExtra(modelID string) map[string]any {
	if m.sessionOptions == nil || m.sessionOptions.AnthropicPromptCache == "" {
		return nil
	}
	index, _, found := gatewayModelLocation(m.gatewayConfig, modelID)
	if !found {
		return nil
	}
	providerType := m.gatewayConfig.Providers[index].Type
	if providerType != "anthropic" && providerType != "claude" {
		return nil
	}
	return map[string]any{gateway.AnthropicCacheField: m.sessionOptions.AnthropicPromptCache}
}
