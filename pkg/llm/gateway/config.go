// Package gateway exposes configured LLM providers through one
// OpenAI-compatible HTTP surface.
package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

const defaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"
const defaultXAIBaseURL = "https://api.x.ai/v1"

type Config struct {
	Listen                    string           `json:"listen"`
	ModelCacheRefreshInterval string           `json:"model_cache_refresh_interval,omitempty"`
	ModelCacheRefreshTimeout  string           `json:"model_cache_refresh_timeout,omitempty"`
	Providers                 []ProviderConfig `json:"providers"`
}

type ProviderConfig struct {
	ID                     string                               `json:"id"`
	Type                   string                               `json:"type"`
	Kind                   string                               `json:"kind,omitempty"`
	Prefix                 string                               `json:"prefix"`
	Enabled                bool                                 `json:"enabled"`
	BaseURL                string                               `json:"base_url,omitempty"`
	APIKey                 string                               `json:"api_key,omitempty"`
	APIKeyEnv              string                               `json:"api_key_env,omitempty"`
	Headers                map[string]string                    `json:"headers,omitempty"`
	Body                   map[string]any                       `json:"body,omitempty"`
	ForwardHeaders         []string                             `json:"forward_headers,omitempty"`
	ForwardResponseHeaders []string                             `json:"forward_response_headers,omitempty"`
	Models                 []string                             `json:"models,omitempty"`
	ModelFilterRegex       string                               `json:"model_filter_regex,omitempty"`
	ModelMetadata          map[string]llmprovider.ModelMetadata `json:"model_metadata,omitempty"`
	Codex                  CodexConfig                          `json:"codex,omitempty"`
	ChatGPT                ChatGPTConfig                        `json:"chatgpt,omitempty"`
}

// ChatGPTConfig contains application identity and a local storage location,
// never credentials or host IDs. Library hosts should supply their own app.
type ChatGPTConfig struct {
	AppID     string `json:"app_id,omitempty"`
	AppName   string `json:"app_name,omitempty"`
	Directory string `json:"directory,omitempty"`
}

type CodexConfig struct {
	Command           string                       `json:"command,omitempty"`
	Args              []string                     `json:"args,omitempty"`
	Environment       map[string]string            `json:"environment,omitempty"`
	Model             string                       `json:"model,omitempty"`
	WorkingDirectory  string                       `json:"working_directory,omitempty"`
	BaseInstructions  string                       `json:"base_instructions,omitempty"`
	Minimal           *bool                        `json:"minimal,omitempty"`
	ThreadStart       map[string]any               `json:"thread_start,omitempty"`
	Sandbox           string                       `json:"sandbox,omitempty"`
	ApprovalPolicy    string                       `json:"approval_policy,omitempty"`
	Ephemeral         *bool                        `json:"ephemeral,omitempty"`
	ServiceName       string                       `json:"service_name,omitempty"`
	ExperimentalAPI   *bool                        `json:"experimental_api,omitempty"`
	ConversationCache CodexConversationCacheConfig `json:"conversation_cache,omitempty"`
}

type CodexConversationCacheConfig struct {
	Type       string           `json:"type,omitempty"`
	TTL        string           `json:"ttl,omitempty"`
	MaxEntries int              `json:"max_entries,omitempty"`
	Redis      CodexRedisConfig `json:"redis,omitempty"`
}

type CodexRedisConfig struct {
	Addresses  []string `json:"addresses,omitempty"`
	Username   string   `json:"username,omitempty"`
	Password   string   `json:"password,omitempty"`
	Database   int      `json:"database,omitempty"`
	ClientName string   `json:"client_name,omitempty"`
	KeyPrefix  string   `json:"key_prefix,omitempty"`
}

func LoadConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("gateway: open config: %w", err)
	}
	defer file.Close()
	var config Config
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("gateway: decode config: %w", err)
	}
	config.expandEnvironment()
	return config, nil
}

func (c *Config) expandEnvironment() {
	c.Listen = os.ExpandEnv(c.Listen)
	c.ModelCacheRefreshInterval = os.ExpandEnv(c.ModelCacheRefreshInterval)
	c.ModelCacheRefreshTimeout = os.ExpandEnv(c.ModelCacheRefreshTimeout)
	for index := range c.Providers {
		provider := &c.Providers[index]
		provider.BaseURL = os.ExpandEnv(provider.BaseURL)
		provider.APIKey = os.ExpandEnv(provider.APIKey)
		provider.ChatGPT.Directory = os.ExpandEnv(provider.ChatGPT.Directory)
		for key, value := range provider.Headers {
			provider.Headers[key] = os.ExpandEnv(value)
		}
		if provider.Body != nil {
			provider.Body = expandAny(provider.Body).(map[string]any)
		}
		for key, value := range provider.Codex.Environment {
			provider.Codex.Environment[key] = os.ExpandEnv(value)
		}
		provider.Codex.WorkingDirectory = os.ExpandEnv(provider.Codex.WorkingDirectory)
		provider.Codex.BaseInstructions = os.ExpandEnv(provider.Codex.BaseInstructions)
		provider.Codex.ConversationCache.Type = os.ExpandEnv(provider.Codex.ConversationCache.Type)
		provider.Codex.ConversationCache.TTL = os.ExpandEnv(provider.Codex.ConversationCache.TTL)
		redis := &provider.Codex.ConversationCache.Redis
		for index := range redis.Addresses {
			redis.Addresses[index] = os.ExpandEnv(redis.Addresses[index])
		}
		redis.Username = os.ExpandEnv(redis.Username)
		redis.Password = os.ExpandEnv(redis.Password)
		redis.ClientName = os.ExpandEnv(redis.ClientName)
		redis.KeyPrefix = os.ExpandEnv(redis.KeyPrefix)
		if provider.Codex.ThreadStart != nil {
			provider.Codex.ThreadStart = expandAny(provider.Codex.ThreadStart).(map[string]any)
		}
	}
}

func expandAny(value any) any {
	switch typed := value.(type) {
	case string:
		return os.ExpandEnv(typed)
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = expandAny(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = expandAny(item)
		}
		return result
	default:
		return value
	}
}

func (p ProviderConfig) validate() error {
	if !p.Enabled {
		return nil
	}
	if p.ID == "" {
		return fmt.Errorf("enabled provider has no id")
	}
	prefix := p.Prefix
	if prefix == "" {
		prefix = p.ID
	}
	if strings.Contains(prefix, "/") {
		return fmt.Errorf("provider %q prefix %q contains '/'", p.ID, prefix)
	}
	switch p.Type {
	case "anthropic", "claude", "codex", "codex-app-server", "grok", "xai", "openrouter", "openai-compatible", "chatgpt":
	default:
		return fmt.Errorf("provider %q has unsupported type %q", p.ID, p.Type)
	}
	if strings.TrimSpace(p.ModelFilterRegex) != "" {
		if _, err := regexp.Compile(p.ModelFilterRegex); err != nil {
			return fmt.Errorf("provider %q model_filter_regex: %w", p.ID, err)
		}
	}
	providerKind, supported := canonicalProviderKind(p.Kind)
	if !supported {
		return fmt.Errorf("provider %q has unsupported kind %q", p.ID, p.Kind)
	}
	if providerKind != "" && !providerTypeAllowsKind(p.Type, providerKind) {
		return fmt.Errorf("provider %q type %q does not support kind %q", p.ID, p.Type, p.Kind)
	}
	if p.Type == "openai-compatible" && p.BaseURL == "" {
		return fmt.Errorf("provider %q requires base_url", p.ID)
	}
	if p.Type == "chatgpt" && (p.APIKey != "" || p.APIKeyEnv != "" || p.BaseURL != "") {
		return fmt.Errorf("provider %q ChatGPT authentication uses its local account store, not base_url or api_key", p.ID)
	}
	if (p.Type == "grok" || p.Type == "xai") && p.APIKey == "" && p.APIKeyEnv == "" {
		return fmt.Errorf("provider %q requires api_key or api_key_env", p.ID)
	}
	if p.Type == "codex" || p.Type == "codex-app-server" {
		cache := p.Codex.ConversationCache
		if cache.Type != "" && cache.Type != "memory" && cache.Type != "redis" {
			return fmt.Errorf("provider %q has unsupported Codex conversation cache type %q", p.ID, cache.Type)
		}
		if cache.MaxEntries < 0 {
			return fmt.Errorf("provider %q Codex conversation cache max_entries cannot be negative", p.ID)
		}
		if cache.TTL != "" {
			ttl, err := time.ParseDuration(cache.TTL)
			if err != nil || ttl <= 0 {
				return fmt.Errorf("provider %q Codex conversation cache ttl must be a positive duration", p.ID)
			}
		}
		if cache.Type == "redis" && len(cache.Redis.Addresses) == 0 {
			return fmt.Errorf("provider %q Redis conversation cache requires addresses", p.ID)
		}
		if cache.Redis.Database < 0 {
			return fmt.Errorf("provider %q Redis conversation cache database cannot be negative", p.ID)
		}
	}
	for model, metadata := range p.ModelMetadata {
		if model == "" {
			return fmt.Errorf("provider %q has metadata for an empty model id", p.ID)
		}
		if metadata.ContextLength < 0 || metadata.MaxOutputTokens < 0 {
			return fmt.Errorf("provider %q model %q metadata values cannot be negative", p.ID, model)
		}
		if metadata.Capabilities == nil || metadata.Capabilities.Reasoning == nil {
			continue
		}
		reasoning := metadata.Capabilities.Reasoning
		if !reasoning.Supported {
			return fmt.Errorf("provider %q model %q has reasoning capabilities with supported=false", p.ID, model)
		}
		switch reasoning.Control {
		case llmprovider.ReasoningControlEffort, llmprovider.ReasoningControlToggle,
			llmprovider.ReasoningControlTokenBudget, llmprovider.ReasoningControlFixed:
		default:
			return fmt.Errorf("provider %q model %q has unsupported reasoning control %q", p.ID, model, reasoning.Control)
		}
		if reasoning.Control != llmprovider.ReasoningControlEffort &&
			len(reasoning.SupportedEfforts) > 0 {
			return fmt.Errorf("provider %q model %q has reasoning efforts for %q control", p.ID, model, reasoning.Control)
		}
		if reasoning.Control != llmprovider.ReasoningControlEffort && reasoning.DefaultEffort != "" {
			return fmt.Errorf("provider %q model %q has a default reasoning effort for %q control", p.ID, model, reasoning.Control)
		}
		if reasoning.Control == llmprovider.ReasoningControlTokenBudget && !reasoning.SupportsMaxTokens {
			return fmt.Errorf("provider %q model %q token-budget control must support max tokens", p.ID, model)
		}
		seenEfforts := make(map[string]struct{}, len(reasoning.SupportedEfforts))
		for _, effort := range reasoning.SupportedEfforts {
			if effort == "" {
				return fmt.Errorf("provider %q model %q has an empty reasoning effort", p.ID, model)
			}
			if _, duplicate := seenEfforts[effort]; duplicate {
				return fmt.Errorf("provider %q model %q has duplicate reasoning effort %q", p.ID, model, effort)
			}
			seenEfforts[effort] = struct{}{}
		}
		if reasoning.DefaultEffort != "" && len(seenEfforts) > 0 {
			if _, supported := seenEfforts[reasoning.DefaultEffort]; !supported {
				return fmt.Errorf("provider %q model %q default reasoning effort %q is not supported", p.ID, model, reasoning.DefaultEffort)
			}
		}
	}
	return nil
}

func canonicalProviderKind(kind string) (string, bool) {
	switch kind {
	case "":
		return "", true
	case "generic":
		return "generic", true
	case "openai", "openai-compatible":
		return "openai", true
	case "openrouter":
		return "openrouter", true
	case "grok", "xai":
		return "grok", true
	case "anthropic", "claude":
		return "anthropic", true
	case "codex", "codex-app-server":
		return "codex", true
	case "chatgpt":
		return "chatgpt", true
	default:
		return "", false
	}
}

func providerTypeAllowsKind(providerType, providerKind string) bool {
	if providerType == "openai-compatible" {
		return true
	}
	if providerKind == "generic" {
		return false
	}
	typeKind, supported := canonicalProviderKind(providerType)
	return supported && providerKind == typeKind
}
