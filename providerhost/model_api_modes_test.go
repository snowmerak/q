package providerhost

import (
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
)

func TestPreferredProviderAPIModesOnlySelectsKnownNativeRoutes(t *testing.T) {
	routes := gateway.Config{Providers: []gateway.ProviderConfig{
		{ID: "openai", Type: "openai-compatible", BaseURL: "https://api.openai.com/v1", Enabled: true},
		{ID: "router", Prefix: "openrouter", Type: "openrouter", Enabled: true},
		{ID: "xai", Type: "xai", Enabled: true},
		{ID: "claude", Type: "anthropic", Enabled: true},
		{ID: "codex", Type: "codex", Enabled: true},
		{ID: "local", Type: "openai-compatible", BaseURL: "http://127.0.0.1:1234/v1", Enabled: true},
		{ID: "disabled", Type: "openrouter"},
	}}
	modes := PreferredProviderAPIModes(routes)
	for _, prefix := range []string{"openai", "openrouter", "xai"} {
		if modes[prefix] != "responses" {
			t.Fatalf("%s mode = %q", prefix, modes[prefix])
		}
	}
	for _, prefix := range []string{"claude", "codex", "local", "disabled"} {
		if modes[prefix] != "" {
			t.Fatalf("%s mode = %q", prefix, modes[prefix])
		}
	}
	value := config.Default()
	value.ModelAPIModes = map[string]string{"openai/chat-only": "chat_completions", "local/manual": "responses"}
	value.ModelGroups = map[string]config.ModelGroupConfig{
		"single": {Candidates: []config.ModelCandidateConfig{{Model: "openai/gpt-5-nano"}}},
		"multi":  {Candidates: []config.ModelCandidateConfig{{Model: "openai/gpt-5-nano"}, {Model: "xai/grok-4.5"}}},
	}
	if PreferredModelAPIMode(value, modes, "openai/chat-only") != "chat_completions" || PreferredModelAPIMode(value, modes, "openai/gpt-5-nano") != "responses" || PreferredModelAPIMode(value, modes, "codex/gpt-6-sol") != "chat_completions" {
		t.Fatal("model override or provider preference did not resolve")
	}
	effective := value.EffectiveModelAPIModes()
	if effective["group/single"] != "chat_completions" || effective["group/multi"] != "chat_completions" || effective["openai/chat-only"] != "chat_completions" || effective["local/manual"] != "responses" {
		t.Fatalf("effective modes = %#v", effective)
	}
}
