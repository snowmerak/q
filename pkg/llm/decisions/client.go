// Package decisions wraps github.com/snowmerak/decisions with Q v2's
// multi-provider configuration, model discovery, and environment-backed auth.
package decisions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	sdk "github.com/snowmerak/decisions"
)

var (
	ErrUnknownModel = errors.New("decisions: model is not configured")
	ErrInvalidCfg   = errors.New("decisions: invalid configuration")
)

// ProviderConfig defines one Decisions API connection.
type ProviderConfig struct {
	ID               string `json:"id"`
	Label            string `json:"label,omitempty"`
	Protocol         string `json:"protocol"` // "openai" | "jev" | "gemini"
	BaseURL          string `json:"base_url"`
	APIKeyEnv        string `json:"api_key_env,omitempty"`
	ModelFilterRegex string `json:"model_filter_regex,omitempty"`
	TimeoutMS        int    `json:"timeout_ms,omitempty"`
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

// Validate verifies provider/model uniqueness, protocol support, regex syntax, and default target.
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
		if pattern := strings.TrimSpace(p.ModelFilterRegex); pattern != "" {
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("%w: provider %q model_filter_regex: %v", ErrInvalidCfg, id, err)
			}
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
			providerID, upstreamModel, hasSlash := strings.Cut(def, "/")
			if !hasSlash || strings.TrimSpace(providerID) == "" || strings.TrimSpace(upstreamModel) == "" {
				return fmt.Errorf("%w: default_model_id %q is not in models", ErrInvalidCfg, def)
			}
			if _, ok := providers[strings.TrimSpace(providerID)]; !ok {
				return fmt.Errorf("%w: default_model_id %q references unknown provider %q", ErrInvalidCfg, def, providerID)
			}
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

// Registry resolves configured or discovered Decisions model IDs to ready SDK clients.
type Registry struct {
	defaultModelID string
	providers      map[string]*providerEntry
	providerOrder  []*providerEntry
	entries        map[string]registryEntry
	staticList     []ModelInfo
}

type providerEntry struct {
	id             string
	label          string
	protocol       string
	api            sdk.API
	baseURL        string
	apiKey         string
	httpClient     *http.Client
	listClient     *sdk.Client
	modelFilter    *regexp.Regexp
	mu             sync.RWMutex
	discovered     []ModelInfo
	dynamicClients map[string]*sdk.Client
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
	providers := make(map[string]*providerEntry, len(cfg.Providers))
	providerOrder := make([]*providerEntry, 0, len(cfg.Providers))

	for _, p := range cfg.Providers {
		pid := strings.TrimSpace(p.ID)
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
				return nil, fmt.Errorf("%w: environment variable %s for provider %q is empty", ErrInvalidCfg, envName, pid)
			}
		}
		timeout := 30 * time.Second
		if p.TimeoutMS > 0 {
			timeout = time.Duration(p.TimeoutMS) * time.Millisecond
		}
		httpClient := &http.Client{
			Timeout:       timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		var modelFilter *regexp.Regexp
		if pattern := strings.TrimSpace(p.ModelFilterRegex); pattern != "" {
			modelFilter, err = regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("%w: provider %q model_filter_regex: %v", ErrInvalidCfg, pid, err)
			}
		}
		baseURL := strings.TrimSpace(p.BaseURL)
		listClient, err := sdk.NewClient(sdk.Config{
			API:        api,
			BaseURL:    baseURL,
			APIKey:     apiKey,
			HTTPClient: httpClient,
		})
		if err != nil {
			return nil, fmt.Errorf("%w: provider %q: %v", ErrInvalidCfg, pid, err)
		}
		pLabel := strings.TrimSpace(p.Label)
		if pLabel == "" {
			pLabel = pid
		}
		pe := &providerEntry{
			id:             pid,
			label:          pLabel,
			protocol:       strings.ToLower(strings.TrimSpace(p.Protocol)),
			api:            api,
			baseURL:        baseURL,
			apiKey:         apiKey,
			httpClient:     httpClient,
			listClient:     listClient,
			modelFilter:    modelFilter,
			dynamicClients: make(map[string]*sdk.Client),
		}
		providers[pid] = pe
		providerOrder = append(providerOrder, pe)
	}

	entries := make(map[string]registryEntry, len(cfg.Models))
	staticList := make([]ModelInfo, 0, len(cfg.Models))
	for _, m := range cfg.Models {
		pe := providers[strings.TrimSpace(m.ProviderID)]
		upstreamModel := strings.TrimSpace(m.Model)
		client, err := sdk.NewClient(sdk.Config{
			API:        pe.api,
			BaseURL:    pe.baseURL,
			APIKey:     pe.apiKey,
			Model:      upstreamModel,
			HTTPClient: pe.httpClient,
		})
		if err != nil {
			return nil, fmt.Errorf("%w: model %q: %v", ErrInvalidCfg, m.ID, err)
		}
		label := strings.TrimSpace(m.Label)
		if label == "" {
			label = strings.TrimSpace(m.ID)
		}
		info := ModelInfo{
			ID:            strings.TrimSpace(m.ID),
			Label:         label,
			ProviderID:    pe.id,
			ProviderLabel: pe.label,
			Protocol:      pe.protocol,
			Model:         upstreamModel,
			ContextWindow: m.ContextWindow,
		}
		entries[info.ID] = registryEntry{model: m, info: info, client: client}
		staticList = append(staticList, info)
	}

	return &Registry{
		defaultModelID: strings.TrimSpace(cfg.DefaultModelID),
		providers:      providers,
		providerOrder:  providerOrder,
		entries:        entries,
		staticList:     staticList,
	}, nil
}

// RefreshModels queries each configured provider's upstream model catalog, applies
// protocol capability filtering (e.g. Gemini createDecision) and ModelFilterRegex,
// and updates the cached discovered list.
func (r *Registry) RefreshModels(ctx context.Context) error {
	if r == nil || len(r.providerOrder) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var wg sync.WaitGroup
	errs := make([]error, len(r.providerOrder))
	for idx, pe := range r.providerOrder {
		wg.Add(1)
		go func(i int, p *providerEntry) {
			defer wg.Done()
			listed, err := p.listClient.ListModels(ctx)
			if err != nil {
				errs[i] = fmt.Errorf("decisions: list models for %q: %w", p.id, err)
				return
			}
			discovered := make([]ModelInfo, 0, len(listed))
			for _, m := range listed {
				if p.api == sdk.GeminiDecisionAPI && m.SupportedMethods != nil && !slices.Contains(m.SupportedMethods, "createDecision") {
					continue
				}
				if p.modelFilter != nil && !p.modelFilter.MatchString(m.ID) {
					continue
				}
				label := strings.TrimSpace(m.DisplayName)
				if label == "" {
					label = m.ID
				}
				discovered = append(discovered, ModelInfo{
					ID:            p.id + "/" + m.ID,
					Label:         label,
					ProviderID:    p.id,
					ProviderLabel: p.label,
					Protocol:      p.protocol,
					Model:         m.ID,
				})
			}
			p.mu.Lock()
			p.discovered = discovered
			p.mu.Unlock()
		}(idx, pe)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// DefaultModelID returns the configured default model ID, if any.
func (r *Registry) DefaultModelID() string {
	if r == nil {
		return ""
	}
	return r.defaultModelID
}

// ListModels returns sanitized metadata for static models and discovered provider models.
func (r *Registry) ListModels() []ModelInfo {
	if r == nil {
		return nil
	}
	out := make([]ModelInfo, 0, len(r.staticList))
	seen := make(map[string]struct{}, len(r.staticList))
	for _, info := range r.staticList {
		out = append(out, info)
		seen[info.ID] = struct{}{}
	}
	for _, p := range r.providerOrder {
		p.mu.RLock()
		discovered := append([]ModelInfo(nil), p.discovered...)
		p.mu.RUnlock()
		for _, info := range discovered {
			if _, ok := seen[info.ID]; ok {
				continue
			}
			seen[info.ID] = struct{}{}
			out = append(out, info)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Resolve returns the SDK client and configuration for modelID.
// When modelID is empty, the configured DefaultModelID is used.
// It checks explicit static aliases first, then resolves "<provider_id>/<upstream_model>".
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
	if entry, ok := r.entries[id]; ok {
		return entry.client, entry.model, nil
	}
	providerID, upstreamModel, hasSlash := strings.Cut(id, "/")
	if hasSlash {
		providerID = strings.TrimSpace(providerID)
		upstreamModel = strings.TrimSpace(upstreamModel)
		if p, ok := r.providers[providerID]; ok && upstreamModel != "" {
			client, err := p.clientForModel(upstreamModel)
			if err != nil {
				return nil, ModelConfig{}, err
			}
			return client, ModelConfig{
				ID:         providerID + "/" + upstreamModel,
				Label:      upstreamModel,
				ProviderID: providerID,
				Model:      upstreamModel,
			}, nil
		}
	}
	return nil, ModelConfig{}, fmt.Errorf("%w: %q", ErrUnknownModel, id)
}

func (p *providerEntry) clientForModel(upstreamModel string) (*sdk.Client, error) {
	p.mu.RLock()
	existing := p.dynamicClients[upstreamModel]
	p.mu.RUnlock()
	if existing != nil {
		return existing, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if existing = p.dynamicClients[upstreamModel]; existing != nil {
		return existing, nil
	}
	client, err := sdk.NewClient(sdk.Config{
		API:        p.api,
		BaseURL:    p.baseURL,
		APIKey:     p.apiKey,
		Model:      upstreamModel,
		HTTPClient: p.httpClient,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: model %q: %v", ErrInvalidCfg, p.id+"/"+upstreamModel, err)
	}
	p.dynamicClients[upstreamModel] = client
	return client, nil
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
