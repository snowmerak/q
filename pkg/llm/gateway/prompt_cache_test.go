package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func TestPrepareResponseCacheMapsAndStripsGatewayAffinity(t *testing.T) {
	for _, test := range []struct {
		name  string
		route *route
		key   string
	}{
		{"openai", &route{providerType: "openai-compatible"}, "prompt_cache_key"},
		{"openrouter", &route{providerType: "openrouter"}, "session_id"},
		{"xai", &route{providerType: "openai-compatible", modelCapabilityProfile: "xai"}, "prompt_cache_key"},
		{"codex", &route{providerType: "codex"}, ""},
		{"chatgpt", &route{providerType: "chatgpt"}, "prompt_cache_key"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := prepareResponseCache(test.route, []byte(`{"model":"m","input":"hi","cache_affinity_id":"cache_test"}`))
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatal(err)
			}
			if _, found := fields["cache_affinity_id"]; found {
				t.Fatalf("Gateway field leaked: %#v", fields)
			}
			if test.key != "" && fields[test.key] != "cache_test" {
				t.Fatalf("affinity = %#v", fields)
			}
		})
	}
	for _, body := range []string{
		`{"model":"m","input":"hi","cache_affinity_id":"cache_auto","prompt_cache_key":"explicit"}`,
	} {
		prepared, err := prepareResponseCache(&route{providerType: "openai-compatible"}, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		_ = json.Unmarshal(prepared, &fields)
		if fields["prompt_cache_key"] != "explicit" {
			t.Fatalf("explicit cache key overwritten: %#v", fields)
		}
	}
}

func TestPrepareResponseCacheEnablesOpenRouterClaudeCaching(t *testing.T) {
	for _, test := range []struct {
		name        string
		route       *route
		body        string
		wantControl bool
		wantType    string
	}{
		{"Claude default", &route{providerType: "openrouter"}, `{"model":"anthropic/claude-haiku-4.5","input":"hi"}`, true, "ephemeral"},
		{"explicit request", &route{providerType: "openrouter"}, `{"model":"anthropic/claude-haiku-4.5","input":"hi","cache_control":{"type":"ephemeral","ttl":"1h"}}`, true, "ephemeral"},
		{"provider configured", &route{providerType: "openrouter", responseCacheControlConfigured: true}, `{"model":"anthropic/claude-haiku-4.5","input":"hi"}`, false, ""},
		{"other model", &route{providerType: "openrouter"}, `{"model":"openai/gpt-5-nano","input":"hi"}`, false, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			prepared, err := prepareResponseCache(test.route, []byte(test.body))
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(prepared, &fields); err != nil {
				t.Fatal(err)
			}
			control, found := fields["cache_control"].(map[string]any)
			if found != test.wantControl || (found && control["type"] != test.wantType) {
				t.Fatalf("cache_control = %#v", fields["cache_control"])
			}
			if test.name == "explicit request" && control["ttl"] != "1h" {
				t.Fatalf("request cache TTL was overwritten: %#v", control)
			}
		})
	}
}

func TestPreparePromptCacheMapsConversationAffinity(t *testing.T) {
	tests := []struct {
		name       string
		route      *route
		extraField string
		header     string
	}{
		{name: "OpenAI compatible", route: &route{providerType: "openai-compatible"}, extraField: "prompt_cache_key"},
		{name: "ChatGPT", route: &route{providerType: "chatgpt"}, extraField: "prompt_cache_key"},
		{name: "OpenRouter", route: &route{providerType: "openrouter"}, extraField: "session_id"},
		{name: "Grok", route: &route{providerType: "grok"}, header: "X-Grok-Conv-Id"},
		{name: "xAI compatible", route: &route{providerType: "openai-compatible", modelCapabilityProfile: "xai"}, header: "X-Grok-Conv-Id"},
		{name: "explicit Grok kind", route: &route{providerType: "openai-compatible", providerKind: "grok"}, header: "X-Grok-Conv-Id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared, conversationID, err := preparePromptCache(test.route, llmprovider.ChatRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(conversationID, gatewayCacheConversationPrefix) {
				t.Fatalf("conversation ID = %q", conversationID)
			}
			if test.extraField != "" && prepared.Extra[test.extraField] != conversationID {
				t.Fatalf("extra = %#v", prepared.Extra)
			}
			if test.header != "" && prepared.Headers.Get(test.header) != conversationID {
				t.Fatalf("headers = %#v", prepared.Headers)
			}

			prepared, secondID, err := preparePromptCache(test.route, llmprovider.ChatRequest{ConversationID: conversationID})
			if err != nil {
				t.Fatal(err)
			}
			if secondID != conversationID || prepared.ConversationID != "" {
				t.Fatalf("second request ID = %q, upstream conversation ID = %q", secondID, prepared.ConversationID)
			}
			if test.extraField != "" && prepared.Extra[test.extraField] != conversationID {
				t.Fatalf("second extra = %#v", prepared.Extra)
			}
			if test.header != "" && prepared.Headers.Get(test.header) != conversationID {
				t.Fatalf("second headers = %#v", prepared.Headers)
			}
		})
	}
}

func TestPreparePromptCachePreservesExplicitSettings(t *testing.T) {
	extra := map[string]any{"prompt_cache_key": "explicit"}
	request := llmprovider.ChatRequest{ConversationID: "provider-thread", Extra: extra}
	prepared, conversationID, err := preparePromptCache(&route{providerType: "openai-compatible"}, request)
	if err != nil {
		t.Fatal(err)
	}
	if conversationID != "provider-thread" || prepared.ConversationID != "provider-thread" ||
		prepared.Extra["prompt_cache_key"] != "explicit" {
		t.Fatalf("prepared = %#v, conversation ID = %q", prepared, conversationID)
	}
	headers := http.Header{"X-Grok-Conv-Id": []string{"explicit-grok"}}
	prepared, _, err = preparePromptCache(&route{providerType: "grok"}, llmprovider.ChatRequest{
		ConversationID: "provider-thread", Headers: headers,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Headers.Get("X-Grok-Conv-Id") != "explicit-grok" {
		t.Fatalf("headers = %#v", prepared.Headers)
	}
}

func TestPreparePromptCacheStripsSyntheticConversationWithExplicitSettings(t *testing.T) {
	for _, test := range []struct {
		name  string
		route *route
		req   llmprovider.ChatRequest
	}{
		{
			name:  "request setting",
			route: &route{providerType: "openai-compatible"},
			req: llmprovider.ChatRequest{
				ConversationID: gatewayCacheConversationPrefix + "request",
				Extra:          map[string]any{"prompt_cache_key": "explicit"},
			},
		},
		{
			name:  "provider setting",
			route: &route{providerType: "openai-compatible", cacheAffinityConfigured: true},
			req:   llmprovider.ChatRequest{ConversationID: gatewayCacheConversationPrefix + "provider"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			prepared, conversationID, err := preparePromptCache(test.route, test.req)
			if err != nil {
				t.Fatal(err)
			}
			if prepared.ConversationID != "" || conversationID != test.req.ConversationID {
				t.Fatalf("prepared conversation ID = %q, lifecycle ID = %q", prepared.ConversationID, conversationID)
			}
		})
	}
}

func TestPreparePromptCacheDropsSyntheticConversationForGenericRoute(t *testing.T) {
	prepared, conversationID, err := preparePromptCache(
		&route{providerType: "openai-compatible", providerKind: "generic"},
		llmprovider.ChatRequest{ConversationID: gatewayCacheConversationPrefix + "old"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ConversationID != "" || conversationID != "" || prepared.Extra != nil {
		t.Fatalf("prepared = %#v, lifecycle ID = %q", prepared, conversationID)
	}
}

func TestPreparePromptCacheOpenRouterClaudeUsesOnlySessionAffinity(t *testing.T) {
	prepared, conversationID, err := preparePromptCache(&route{providerType: "openrouter"}, llmprovider.ChatRequest{
		Model: "anthropic/claude-sonnet-4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Extra["session_id"] != conversationID {
		t.Fatalf("session affinity = %#v, lifecycle ID = %q", prepared.Extra["session_id"], conversationID)
	}
	if _, configured := prepared.Extra["cache_control"]; configured {
		t.Fatalf("OpenRouter Claude unexpectedly enabled Anthropic caching: %#v", prepared.Extra)
	}
}

func TestPreparePromptCacheDoesNotOverrideExplicitAffinity(t *testing.T) {
	tests := []struct {
		name    string
		route   *route
		request llmprovider.ChatRequest
	}{
		{name: "request OpenAI key", route: &route{providerType: "openai-compatible"}, request: llmprovider.ChatRequest{Extra: map[string]any{"prompt_cache_key": "explicit"}}},
		{name: "request OpenRouter session", route: &route{providerType: "openrouter"}, request: llmprovider.ChatRequest{Extra: map[string]any{"session_id": "explicit"}}},
		{name: "request Grok header", route: &route{providerType: "grok"}, request: llmprovider.ChatRequest{Headers: http.Header{"X-Grok-Conv-Id": []string{"explicit"}}}},
		{name: "provider default", route: &route{providerType: "openai-compatible", cacheAffinityConfigured: true}, request: llmprovider.ChatRequest{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared, conversationID, err := preparePromptCache(test.route, test.request)
			if err != nil {
				t.Fatal(err)
			}
			if conversationID != "" || prepared.ConversationID != "" {
				t.Fatalf("prepared = %#v, conversation ID = %q", prepared, conversationID)
			}
		})
	}
}

func TestPreparePromptCacheEnablesAnthropicAutomaticCaching(t *testing.T) {
	for _, testRoute := range []*route{
		{providerType: "anthropic"},
		{providerType: "claude"},
		{providerType: "openai-compatible", providerKind: "anthropic"},
	} {
		prepared, conversationID, err := preparePromptCache(testRoute, llmprovider.ChatRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(conversationID, gatewayCacheConversationPrefix) {
			t.Fatalf("conversation ID = %q", conversationID)
		}
		cacheControl, ok := prepared.Extra["cache_control"].(map[string]any)
		if !ok || cacheControl["type"] != "ephemeral" {
			t.Fatalf("cache control = %#v", prepared.Extra["cache_control"])
		}

		prepared, secondID, err := preparePromptCache(testRoute, llmprovider.ChatRequest{ConversationID: conversationID})
		if err != nil {
			t.Fatal(err)
		}
		if secondID != conversationID || prepared.ConversationID != "" {
			t.Fatalf("second request ID = %q, upstream conversation ID = %q", secondID, prepared.ConversationID)
		}
		cacheControl, ok = prepared.Extra["cache_control"].(map[string]any)
		if !ok || cacheControl["type"] != "ephemeral" {
			t.Fatalf("second cache control = %#v", prepared.Extra["cache_control"])
		}
	}
}

func TestPreparePromptCachePreservesExplicitAnthropicCacheControl(t *testing.T) {
	explicit := map[string]any{"type": "ephemeral", "ttl": "1h"}
	prepared, conversationID, err := preparePromptCache(&route{providerType: "anthropic"}, llmprovider.ChatRequest{
		Extra: map[string]any{"cache_control": explicit},
	})
	if err != nil {
		t.Fatal(err)
	}
	cacheControl, ok := prepared.Extra["cache_control"].(map[string]any)
	if !strings.HasPrefix(conversationID, gatewayCacheConversationPrefix) || !ok ||
		cacheControl["type"] != "ephemeral" || cacheControl["ttl"] != "1h" {
		t.Fatalf("prepared = %#v, conversation ID = %q", prepared, conversationID)
	}

	prepared, conversationID, err = preparePromptCache(&route{
		providerType: "anthropic", cacheAffinityConfigured: true,
	}, llmprovider.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(conversationID, gatewayCacheConversationPrefix) || prepared.Extra != nil {
		t.Fatalf("provider default was overridden: %#v, conversation ID = %q", prepared, conversationID)
	}
}

func TestPreparePromptCacheLeavesCodexAlone(t *testing.T) {
	for _, providerType := range []string{"codex", "codex-app-server"} {
		request := llmprovider.ChatRequest{ConversationID: "existing"}
		prepared, conversationID, err := preparePromptCache(&route{providerType: providerType}, request)
		if err != nil {
			t.Fatal(err)
		}
		if conversationID != "existing" || prepared.ConversationID != "existing" ||
			prepared.Extra != nil || prepared.Headers != nil {
			t.Fatalf("%s prepared = %#v, conversation ID = %q", providerType, prepared, conversationID)
		}
	}

	generated, err := newCacheConversationID()
	if err != nil {
		t.Fatal(err)
	}
	for _, providerType := range []string{"codex", "codex-app-server"} {
		prepared, conversationID, err := preparePromptCache(&route{providerType: providerType}, llmprovider.ChatRequest{ConversationID: generated})
		if err != nil {
			t.Fatal(err)
		}
		if conversationID != "" || prepared.ConversationID != "" {
			t.Fatalf("%s treated cache affinity as a Codex thread: %#v", providerType, prepared)
		}
	}
}

func TestPreparePromptCacheAnthropicPolicy(t *testing.T) {
	anthropicRoute := &route{providerType: "anthropic"}
	for _, test := range []struct {
		policy  string
		wantTTL string
	}{
		{AnthropicCacheOneHour, "1h"},
		{AnthropicCacheOff, ""},
	} {
		request := llmprovider.ChatRequest{ConversationID: "cache_previous", Extra: map[string]any{AnthropicCacheField: test.policy}}
		prepared, id, err := preparePromptCache(anthropicRoute, request)
		if err != nil {
			t.Fatal(err)
		}
		if prepared.ConversationID != "" || (test.policy == AnthropicCacheOff && id != "") {
			t.Fatalf("policy %q: request = %#v, id = %q", test.policy, prepared, id)
		}
		if _, leaked := prepared.Extra[AnthropicCacheField]; leaked {
			t.Fatalf("Gateway extension leaked: %#v", prepared.Extra)
		}
		control, _ := prepared.Extra["cache_control"].(map[string]any)
		if test.wantTTL == "" {
			if _, configured := prepared.Extra["cache_control"]; configured {
				t.Fatalf("off still has cache control: %#v", prepared.Extra)
			}
		} else if control["ttl"] != test.wantTTL || control["type"] != "ephemeral" {
			t.Fatalf("policy %q: cache control = %#v", test.policy, control)
		}
	}
	configured := &route{providerType: "anthropic", cacheAffinityConfigured: true}
	prepared, _, err := preparePromptCache(configured, llmprovider.ChatRequest{Extra: map[string]any{AnthropicCacheField: AnthropicCacheOff}})
	if err != nil {
		t.Fatal(err)
	}
	if value, present := prepared.Extra["cache_control"]; !present || value != nil {
		t.Fatalf("provider cache default was not suppressed: %#v", prepared.Extra)
	}
}

func TestPreparePromptCacheRejectsInvalidAnthropicPolicy(t *testing.T) {
	for _, test := range []struct {
		route   *route
		request llmprovider.ChatRequest
	}{
		{&route{providerType: "openai-compatible"}, llmprovider.ChatRequest{Extra: map[string]any{AnthropicCacheField: AnthropicCacheOneHour}}},
		{&route{providerType: "anthropic"}, llmprovider.ChatRequest{Extra: map[string]any{AnthropicCacheField: "2h"}}},
		{&route{providerType: "anthropic"}, llmprovider.ChatRequest{Extra: map[string]any{AnthropicCacheField: AnthropicCacheOff, "cache_control": map[string]any{"type": "ephemeral"}}}},
	} {
		if _, _, err := preparePromptCache(test.route, test.request); err == nil {
			t.Fatalf("accepted invalid policy: %#v", test.request)
		}
	}
}
