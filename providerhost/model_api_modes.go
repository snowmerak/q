package providerhost

import (
	"strings"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
)

// PreferredProviderAPIModes maps saved provider options to Gateway model prefixes.
// Missing options use Chat Completions; provider identity is never reinterpreted
// while constructing a client. The optional config preserves existing callers.
func PreferredProviderAPIModes(gatewayConfig gateway.Config, configured ...config.Config) map[string]string {
	modes := make(map[string]string)
	if len(configured) == 0 {
		return modes
	}
	for _, provider := range gatewayConfig.Providers {
		if !provider.Enabled {
			continue
		}
		mode := configured[0].ProviderAPIModes[provider.ID]
		if mode == "" {
			continue
		}
		prefix := provider.Prefix
		if prefix == "" {
			prefix = provider.ID
		}
		modes[prefix] = mode
	}
	return modes
}

// ProviderAPIMode is the saved Q API option for a provider, independent of kind.
func ProviderAPIMode(value config.Config, id string) string {
	if mode := value.ProviderAPIModes[id]; mode != "" {
		return mode
	}
	return "chat_completions"
}

// InitialProviderAPIMode is only used when choosing an explicit provider kind.
// It is a settings initializer, never a request-time override or URL inference.
func InitialProviderAPIMode(provider gateway.ProviderConfig) string {
	kind := provider.Kind
	if kind == "" {
		kind = provider.Type
	}
	switch kind {
	case "openai", "openrouter", "xai", "grok":
		return "responses"
	}
	return "chat_completions"
}

// PreferredModelAPIMode applies a model override before the saved provider option.
func PreferredModelAPIMode(value config.Config, providerModes map[string]string, model string) string {
	if mode := value.ModelAPIModes[model]; mode != "" {
		return mode
	}
	if prefix, _, found := strings.Cut(model, "/"); found {
		if mode := providerModes[prefix]; mode != "" {
			return mode
		}
	}
	return "chat_completions"
}
