package providerhost

import (
	"net/url"
	"strings"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
)

// PreferredProviderAPIModes selects Responses for Gateway routes whose upstream
// API is known to support native Responses. Unknown compatible endpoints stay
// on Chat Completions until a concrete model is explicitly configured.
func PreferredProviderAPIModes(gatewayConfig gateway.Config) map[string]string {
	modes := make(map[string]string)
	for _, provider := range gatewayConfig.Providers {
		if !provider.Enabled || !knownNativeResponsesProvider(provider) {
			continue
		}
		prefix := provider.Prefix
		if prefix == "" {
			prefix = provider.ID
		}
		modes[prefix] = "responses"
	}
	return modes
}

func knownNativeResponsesProvider(provider gateway.ProviderConfig) bool {
	switch provider.Type {
	case "openrouter", "xai", "grok":
		return true
	case "openai-compatible":
		switch provider.Kind {
		case "openai", "openai-compatible", "openrouter", "xai", "grok":
			return true
		case "", "generic":
			if provider.Kind == "generic" {
				return false
			}
			parsed, err := url.Parse(provider.BaseURL)
			if err != nil || parsed.Scheme != "https" {
				return false
			}
			switch strings.ToLower(parsed.Hostname()) {
			case "api.openai.com", "api.x.ai", "openrouter.ai":
				return true
			}
		}
	}
	return false
}

// PreferredModelAPIMode applies a model override before the Gateway default.
func PreferredModelAPIMode(value config.Config, providerModes map[string]string, model string) string {
	if mode := value.ModelAPIModes[model]; mode != "" {
		return mode
	}
	if prefix, _, found := strings.Cut(model, "/"); found && providerModes[prefix] == "responses" {
		return "responses"
	}
	return "chat_completions"
}
