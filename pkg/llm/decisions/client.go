// Package decisions wraps github.com/snowmerak/decisions with Q v2's
// multi-provider configuration, model resolution, and environment-backed auth.
package decisions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdk "github.com/snowmerak/decisions"
)

var (
	ErrUnknownModel = errors.New("decisions: model is not configured")
	ErrInvalidCfg   = errors.New("decisions: invalid configuration")
)

// ProviderConfig defines one Decisions API connection.
type ProviderConfig struct {
	ID        string `json:"id"`
	Label     string `json:"label,omitempty"`
	Protocol  string `json:"protocol"` // "openai" | "jev" | "gemini"
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

// ModelConfig defines one selectable Decisions model backed by a provider.
type ModelConfig struct {
	ID            string `json:"id"`
	Label         string `json:"label,omitempty"`
	ProviderID    string `json:"provider_id"`
	Model         string `json:"model"`
	ContextWindow int    `json:"context_window,omitempty"`
}

// Config configures Decisions providers, models, and the server default model.
type Config struct {
	Providers      []ProviderConfig `json:"providers"`
	Models         []ModelConfig    `json:"models"`
	DefaultModelID string           `json:"default_model_id,omitempty"`
}

// ModelInfo is the public metadata exposed by runtime model listing endpoints.
// It intentionally omits endpoints, raw keys, and environment variable names.
type ModelInfo struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	ProviderID    string `json:"provider_id"`
	ProviderLabel string `json:"provider_label"`
	Protocol      string `json:"protocol"`
	Model         string `json:"model"`
	ContextWindow int    `json:"context_window,omitempty"`
}

// Validate verifies provider/model uniqueness, protocol support, and default target.
func (c Config) Validate() error {
	providers := make(map[string]ProviderConfig, len(c.Providers))
	for _, p := range c.Providers {
		id := strings.TrimSpace(p.ID)
		if id == "" {
			return fmt.Errorf("%w: provider id is required", ErrInvalidCfg)
		}
		if _, dup := providers[id]; dup {
			return fmt.Errorf("%w: duplicate provider id %q", ErrInvalidCfg, id)
		}
		if _, err := parseAPI(p.Protocol); err != nil {
			return fmt.Errorf("%w: provider %q: %v", ErrInvalidCfg, id, err)
		}
		if strings.TrimSpace(p.BaseURL) == "" {
			return fmt.Errorf("%w: provider %q base_url is required", ErrInvalidCfg, id)
		}
		if p.TimeoutMS < 0 || p.TimeoutMS > 3_000_000 {
			return fmt.Errorf("%w: provider %q timeout_ms must be 0..3000000", ErrInvalidCfg, id)
		}
		providers[id] = p
	}

	models := make(map[string]ModelConfig, len(c.Models))
	for _, m := range c.Models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			return fmt.Errorf("%w: model id is required", ErrInvalidCfg)
		}
		if _, dup := models[id]; dup {
			return fmt.Errorf("%w: duplicate model id %q", ErrInvalidCfg, id)
		}
		if _, ok := providers[strings.TrimSpace(m.ProviderID)]; !ok {
			return fmt.Errorf("%w: model %q references unknown provider %q", ErrInvalidCfg, id, m.ProviderID)
		}
		if strings.TrimSpace(m.Model) == "" {
			return fmt.Errorf("%w: model %q upstream model name is required", ErrInvalidCfg, id)
		}
		models[id] = m
	}

	if def := strings.TrimSpace(c.DefaultModelID); def != "" {
		if _, ok := models[def]; !ok {
			return fmt.Errorf("%w: default_model_id %q is not in models", ErrInvalidCfg, def)
		}
	}
	return nil
}

func parseAPI(protocol string) (sdk.API, error) {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "openai":
		return sdk.DecisionAPI, nil
	case "jev", "systemone":
		return sdk.SystemOneAPI, nil
	case "gemini":
		return sdk.GeminiDecisionAPI, nil
	default:
		return "", fmt.Errorf("unsupported protocol %q", protocol)
	}
}

// Registry resolves configured Decisions model IDs to ready SDK clients.
type Registry struct {
	defaultModelID string
	entries        map[string]registryEntry
	list           []ModelInfo
}

type registryEntry struct {
	model  ModelConfig
	info   ModelInfo
	client *sdk.Client
}

// NewRegistry validates cfg and builds SDK clients using lookupEnv for API keys.
// When APIKeyEnv is set on a provider, lookupEnv must return a non-empty value.
func NewRegistry(cfg Config, lookupEnv func(string) string) (*Registry, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	providers := make(map[string]ProviderConfig, len(cfg.Providers))
	for _, p := range cfg.Providers {
		providers[strings.TrimSpace(p.ID)] = p
	}

	entries := make(map[string]registryEntry, len(cfg.Models))
	list := make([]ModelInfo, 0, len(cfg.Models))
	for _, m := range cfg.Models {
		p := providers[strings.TrimSpace(m.ProviderID)]
		api, err := parseAPI(p.Protocol)
		if err != nil {
			return nil, err
		}
		var apiKey string
		if envName := strings.TrimSpace(p.APIKeyEnv); envName != "" {
			if lookupEnv == nil {
				return nil, fmt.Errorf("%w: missing env lookup for %s", ErrInvalidCfg, envName)
			}
			apiKey = strings.TrimSpace(lookupEnv(envName))
			if apiKey == "" {
				return nil, fmt.Errorf("%w: environment variable %s for provider %q is empty", ErrInvalidCfg, envName, p.ID)
			}
		}
		timeout := 30 * time.Second
		if p.TimeoutMS > 0 {
			timeout = time.Duration(p.TimeoutMS) * time.Millisecond
		}
		client, err := sdk.NewClient(sdk.Config{
			API:     api,
			BaseURL: strings.TrimSpace(p.BaseURL),
			APIKey:  apiKey,
			Model:   strings.TrimSpace(m.Model),
			HTTPClient: &http.Client{
				Timeout:       timeout,
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			},
		})
		if err != nil {
			return nil, fmt.Errorf("%w: model %q: %v", ErrInvalidCfg, m.ID, err)
		}
		label := strings.TrimSpace(m.Label)
		if label == "" {
			label = m.ID
		}
		pLabel := strings.TrimSpace(p.Label)
		if pLabel == "" {
			pLabel = p.ID
		}
		info := ModelInfo{
			ID:            strings.TrimSpace(m.ID),
			Label:         label,
			ProviderID:    strings.TrimSpace(p.ID),
			ProviderLabel: pLabel,
			Protocol:      strings.ToLower(strings.TrimSpace(p.Protocol)),
			Model:         strings.TrimSpace(m.Model),
			ContextWindow: m.ContextWindow,
		}
		entries[info.ID] = registryEntry{model: m, info: info, client: client}
		list = append(list, info)
	}

	return &Registry{
		defaultModelID: strings.TrimSpace(cfg.DefaultModelID),
		entries:        entries,
		list:           list,
	}, nil
}

// DefaultModelID returns the configured default model ID, if any.
func (r *Registry) DefaultModelID() string {
	if r == nil {
		return ""
	}
	return r.defaultModelID
}

// ListModels returns sanitized metadata for all configured Decisions models.
func (r *Registry) ListModels() []ModelInfo {
	if r == nil || len(r.list) == 0 {
		return nil
	}
	out := make([]ModelInfo, len(r.list))
	copy(out, r.list)
	return out
}

// Resolve returns the SDK client and configuration for modelID.
// When modelID is empty, the configured DefaultModelID is used.
// Unknown or removed model IDs always fail explicitly without silent fallback.
func (r *Registry) Resolve(modelID string) (*sdk.Client, ModelConfig, error) {
	if r == nil {
		return nil, ModelConfig{}, fmt.Errorf("%w: registry is nil", ErrUnknownModel)
	}
	id := strings.TrimSpace(modelID)
	if id == "" {
		id = r.defaultModelID
	}
	if id == "" {
		return nil, ModelConfig{}, fmt.Errorf("%w: no model id or default configured", ErrUnknownModel)
	}
	entry, ok := r.entries[id]
	if !ok {
		return nil, ModelConfig{}, fmt.Errorf("%w: %q", ErrUnknownModel, id)
	}
	return entry.client, entry.model, nil
}

// Evaluate executes a Decisions evaluation against the resolved modelID.
func (r *Registry) Evaluate(ctx context.Context, modelID string, input sdk.Input, questions sdk.Questions) (*sdk.Response, error) {
	client, _, err := r.Resolve(modelID)
	if err != nil {
		return nil, err
	}
	return client.Evaluate(ctx, sdk.Request{
		Input:     input,
		Questions: questions,
	})
}

// Positive reports whether the named NoulQuestion answer has probability >= 0.5.
func Positive(resp *sdk.Response, name string) (bool, error) {
	if resp == nil {
		return false, errors.New("decisions: nil response")
	}
	ans, ok := resp.Answers[name]
	if !ok {
		return false, fmt.Errorf("decisions: answer %q not found", name)
	}
	noul, ok := ans.(sdk.NoulAnswer)
	if !ok {
		return false, fmt.Errorf("decisions: answer %q is %T, expected NoulAnswer", name, ans)
	}
	return noul.Probability >= 0.5, nil
}
