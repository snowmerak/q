package app

import (
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
)

func TestAnthropicRequestExtraOnlyForSelectedNativeProvider(t *testing.T) {
	m := model{}
	m.sessionOptions = &SessionOptions{AnthropicPromptCache: "1h"}
	m.gatewayConfig = gateway.Config{Providers: []gateway.ProviderConfig{
		{ID: "claude", Type: "anthropic"},
		{ID: "xai", Type: "xai"},
		{ID: "router", Type: "openrouter"},
	}}
	if got := m.anthropicRequestExtra("claude/model")[gateway.AnthropicCacheField]; got != "1h" {
		t.Fatalf("member cache policy = %#v", got)
	}
	for _, modelID := range []string{"xai/model", "router/anthropic/claude", "unknown/model"} {
		if got := m.anthropicRequestExtra(modelID); got != nil {
			t.Fatalf("model %q received Anthropic cache policy: %#v", modelID, got)
		}
	}
	m.sessionOptions.AnthropicPromptCache = "off"
	if got := m.anthropicRequestExtra("claude/model")[gateway.AnthropicCacheField]; got != "off" {
		t.Fatalf("chair cache policy = %#v", got)
	}
	m.sessionOptions = nil
	if got := m.anthropicRequestExtra("claude/model"); got != nil {
		t.Fatalf("normal session received Council cache policy: %#v", got)
	}
}
