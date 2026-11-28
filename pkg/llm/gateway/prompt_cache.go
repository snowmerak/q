package gateway

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

const gatewayCacheConversationPrefix = "cache_"

// AnthropicCacheField is a Gateway-only Chat Completions extension. The Gateway
// consumes it before forwarding the request to Anthropic.
const AnthropicCacheField = "q_anthropic_cache"

const (
	AnthropicCacheOneHour = "1h"
	AnthropicCacheOff     = "off"
)

var errAnthropicCachePolicy = errors.New("invalid Anthropic cache policy")

// preparePromptCache gives HTTP providers the cache metadata appropriate to
// their backend. Codex owns ConversationID as an App Server thread ID. For
// Anthropic, the Gateway-generated ConversationID is only a client-side
// lifecycle marker; Claude keys its cache by the exact prompt prefix and uses
// top-level cache_control to move the breakpoint automatically.
func preparePromptCache(route *route, request llmprovider.ChatRequest) (llmprovider.ChatRequest, string, error) {
	conversationID := request.ConversationID
	cacheConversation := strings.HasPrefix(conversationID, gatewayCacheConversationPrefix)
	if cacheConversation {
		// Gateway-generated IDs are cache affinity identifiers, not upstream
		// stateful-conversation handles. Strip them before every early return so
		// generic and explicitly configured providers never receive them.
		request.ConversationID = ""
	}

	mechanism := promptCacheMechanism(route)
	if policyValue, configured := request.Extra[AnthropicCacheField]; configured {
		if mechanism != "anthropic" {
			return llmprovider.ChatRequest{}, "", fmt.Errorf("%w: %s requires an Anthropic route", errAnthropicCachePolicy, AnthropicCacheField)
		}
		policy, ok := policyValue.(string)
		if !ok || (policy != AnthropicCacheOneHour && policy != AnthropicCacheOff) {
			return llmprovider.ChatRequest{}, "", fmt.Errorf("%w: %s must be %q or %q", errAnthropicCachePolicy, AnthropicCacheField, AnthropicCacheOneHour, AnthropicCacheOff)
		}
		request.Extra = cloneAnyMap(request.Extra)
		delete(request.Extra, AnthropicCacheField)
		if policy == AnthropicCacheOff {
			if hasAnthropicCacheBreakpoint(request) {
				return llmprovider.ChatRequest{}, "", fmt.Errorf("%w: %s=%q conflicts with explicit Anthropic cache controls", errAnthropicCachePolicy, AnthropicCacheField, policy)
			}
			if route.cacheAffinityConfigured {
				// A nil request override removes a provider-level default in the
				// native Anthropic adapter before the request reaches the wire.
				request.Extra["cache_control"] = nil
			}
			if len(request.Extra) == 0 {
				request.Extra = nil
			}
			return request, "", nil
		}
		if hasAnthropicCacheBreakpoint(request) {
			return llmprovider.ChatRequest{}, "", fmt.Errorf("%w: %s=%q conflicts with explicit Anthropic cache controls", errAnthropicCachePolicy, AnthropicCacheField, policy)
		}
		request.Extra["cache_control"] = map[string]any{"type": "ephemeral", "ttl": AnthropicCacheOneHour}
	}
	if mechanism == "" {
		// A model group may move a conversation from a cached route to a route
		// without automatic cache affinity. Do not treat its Gateway cache ID as
		// a provider-native conversation or keep that obsolete lifecycle alive.
		if cacheConversation {
			return request, "", nil
		}
		return request, conversationID, nil
	}
	cacheConfigured := route.cacheAffinityConfigured || requestCacheAffinityConfigured(mechanism, request)
	if mechanism != "anthropic" && cacheConfigured {
		return request, conversationID, nil
	}

	if conversationID == "" {
		var err error
		conversationID, err = newCacheConversationID()
		if err != nil {
			return llmprovider.ChatRequest{}, "", err
		}
	}

	switch mechanism {
	case "openai", "chatgpt":
		request.Extra = cloneAnyMap(request.Extra)
		if _, configured := request.Extra["prompt_cache_key"]; !configured {
			request.Extra["prompt_cache_key"] = conversationID
		}
	case "openrouter":
		request.Extra = cloneAnyMap(request.Extra)
		if _, configured := request.Extra["session_id"]; !configured {
			request.Extra["session_id"] = conversationID
		}
	case "grok":
		request.Headers = request.Headers.Clone()
		if request.Headers == nil {
			request.Headers = make(http.Header)
		}
		if request.Headers.Get("X-Grok-Conv-Id") == "" {
			request.Headers.Set("X-Grok-Conv-Id", conversationID)
		}
	case "anthropic":
		// The native Messages API's automatic cache breakpoint advances to the
		// last cacheable block. Preserve request- or provider-level controls so
		// callers can choose a 1h TTL or explicit block-level breakpoints.
		if !cacheConfigured {
			request.Extra = cloneAnyMap(request.Extra)
			request.Extra["cache_control"] = map[string]any{"type": "ephemeral"}
		}
	}
	return request, conversationID, nil
}

func hasAnthropicCacheBreakpoint(request llmprovider.ChatRequest) bool {
	if _, configured := request.Extra["cache_control"]; configured {
		return true
	}
	for _, message := range request.Messages {
		for _, part := range message.ContentParts {
			if _, configured := part["cache_control"]; configured {
				return true
			}
			if _, configured := part["prompt_cache_breakpoint"]; configured {
				return true
			}
		}
	}
	return false
}

func providerCacheAffinityConfigured(config ProviderConfig, providerKind string) bool {
	mechanism := promptCacheMechanism(&route{providerType: config.Type, providerKind: providerKind})
	switch mechanism {
	case "openai":
		_, configured := config.Body["prompt_cache_key"]
		return configured
	case "openrouter":
		if _, configured := config.Body["session_id"]; configured {
			return true
		}
		return headerValue(config.Headers, "X-Session-Id") != ""
	case "grok":
		return headerValue(config.Headers, "X-Grok-Conv-Id") != ""
	case "anthropic":
		_, configured := config.Body["cache_control"]
		return configured
	default:
		return false
	}
}

func providerResponseCacheConfigured(config ProviderConfig, providerKind string) bool {
	mechanism := promptCacheMechanism(&route{providerType: config.Type, providerKind: providerKind})
	switch mechanism {
	case "openai", "grok":
		_, configured := config.Body["prompt_cache_key"]
		return configured
	case "openrouter":
		_, configured := config.Body["session_id"]
		return configured || headerValue(config.Headers, "X-Session-Id") != ""
	default:
		return false
	}
}

func providerHasResponseCacheControl(config ProviderConfig) bool {
	_, configured := config.Body["cache_control"]
	return configured
}

func requestCacheAffinityConfigured(mechanism string, request llmprovider.ChatRequest) bool {
	switch mechanism {
	case "openai", "chatgpt":
		_, configured := request.Extra["prompt_cache_key"]
		return configured
	case "openrouter":
		if _, configured := request.Extra["session_id"]; configured {
			return true
		}
		return request.Headers.Get("X-Session-Id") != ""
	case "grok":
		return request.Headers.Get("X-Grok-Conv-Id") != ""
	case "anthropic":
		_, configured := request.Extra["cache_control"]
		return configured
	default:
		return false
	}
}

func headerValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func promptCacheMechanism(route *route) string {
	switch routeKind(route) {
	case "grok":
		return "grok"
	case "openrouter":
		return "openrouter"
	case "openai":
		return "openai"
	case "anthropic":
		return "anthropic"
	case "chatgpt":
		return "chatgpt"
	default:
		return ""
	}
}

func routeKind(route *route) string {
	if route == nil {
		return ""
	}
	if route.providerKind != "" {
		return route.providerKind
	}
	if route.modelCapabilityProfile == "xai" || route.providerType == "grok" || route.providerType == "xai" {
		return "grok"
	}
	switch route.providerType {
	case "openrouter":
		return "openrouter"
	case "openai-compatible":
		return "openai"
	case "anthropic", "claude":
		return "anthropic"
	case "codex", "codex-app-server":
		return "codex"
	case "chatgpt":
		return "chatgpt"
	default:
		return ""
	}
}

func newCacheConversationID() (string, error) {
	var entropy [18]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("gateway: generate prompt-cache conversation ID: %w", err)
	}
	return gatewayCacheConversationPrefix + base64.RawURLEncoding.EncodeToString(entropy[:]), nil
}

func cloneAnyMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	return result
}
