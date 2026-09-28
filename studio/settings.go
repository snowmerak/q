package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/gatewayconfig"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/remoteconfig"
	"github.com/snowmerak/q/systemoneconfig"
)

const maximumSettingsRequestSize = 64 << 10

type settingsService struct {
	mu        sync.Mutex
	main      config.Store
	gateway   gatewayconfig.Store
	remote    remoteconfig.Store
	systemOne systemoneconfig.Store
	mcp       mcpconfig.Store
	providers providerhost.Store
}

type settingsSnapshot struct {
	Version      int                 `json:"version"`
	Scope        string              `json:"scope"`
	Runtime      runtimeSettings     `json:"runtime"`
	Models       modelSettings       `json:"models"`
	Providers    providerSettings    `json:"gateway_providers"`
	Services     serviceSettings     `json:"services"`
	Integrations integrationSettings `json:"integrations"`
}

type runtimeSettings struct {
	Configured  bool            `json:"configured"`
	ConfigPath  string          `json:"config_path"`
	MaxParallel int             `json:"max_parallel"`
	Context     contextSettings `json:"context"`
	Loom        loomSettings    `json:"loom"`
}

type contextSettings struct {
	Window       int64   `json:"window"`
	TriggerRatio float64 `json:"trigger_ratio"`
	TargetRatio  float64 `json:"target_ratio"`
	RecentRatio  float64 `json:"recent_ratio"`
}

type loomSettings struct {
	MaximumArtifactMiB int     `json:"maximum_artifact_mib"`
	MaximumStoreMiB    int     `json:"maximum_store_mib"`
	GCDisabled         bool    `json:"gc_disabled"`
	GCTriggerRatio     float64 `json:"gc_trigger_ratio"`
	GCTargetRatio      float64 `json:"gc_target_ratio"`
	GCGraceHours       int     `json:"gc_grace_hours"`
}

type modelSettings struct {
	ConfigPath          string                `json:"config_path"`
	DefaultModel        string                `json:"default_model"`
	DefaultReasoning    string                `json:"default_reasoning_effort"`
	EmbeddingModel      string                `json:"embedding_model"`
	EmbeddingDimensions int                   `json:"embedding_dimensions"`
	GroupCount          int                   `json:"group_count"`
	Roles               []roleModelAssignment `json:"roles"`
}

type roleModelAssignment struct {
	Role            string `json:"role"`
	ConfiguredModel string `json:"configured_model"`
	EffectiveModel  string `json:"effective_model"`
	ReasoningEffort string `json:"reasoning_effort"`
	Inherited       bool   `json:"inherited"`
}

type providerSettings struct {
	ConfigPath string                    `json:"config_path"`
	Items      []gatewayProviderSettings `json:"items"`
}

type gatewayProviderSettings struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	Kind            string `json:"kind"`
	Prefix          string `json:"prefix"`
	Enabled         bool   `json:"enabled"`
	BaseURL         string `json:"base_url"`
	APIKeyEnv       string `json:"api_key_env"`
	HasInlineAPIKey bool   `json:"has_inline_api_key"`
	ModelCount      int    `json:"model_count"`
}

type serviceSettings struct {
	Gateway   listenerSettings  `json:"gateway"`
	SystemOne systemOneSettings `json:"system_one"`
	Remote    remoteSettings    `json:"remote"`
}

type listenerSettings struct {
	ConfigPath    string `json:"config_path"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	ActiveAPIKeys int    `json:"active_api_keys"`
}

type systemOneSettings struct {
	listenerSettings
	ProviderCount  int    `json:"provider_count"`
	DefaultModel   string `json:"default_model"`
	RoleModelCount int    `json:"role_model_count"`
}

type remoteSettings struct {
	listenerSettings
	AuthenticationEnabled bool `json:"authentication_enabled"`
}

type integrationSettings struct {
	MCP integrationSummary `json:"mcp"`
	LSP integrationSummary `json:"lsp"`
}

type integrationSummary struct {
	ConfigPath string `json:"config_path"`
	Items      int    `json:"items"`
	Bindings   int    `json:"bindings"`
}

type runtimeUpdate struct {
	MaxParallel int             `json:"max_parallel"`
	Context     contextSettings `json:"context"`
	Loom        loomSettings    `json:"loom"`
}

type serviceUpdate struct {
	Host                  string `json:"host"`
	Port                  int    `json:"port"`
	AuthenticationEnabled bool   `json:"authentication_enabled,omitempty"`
}

type modelAssignmentUpdate struct {
	Model               string `json:"model"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
	EmbeddingDimensions int    `json:"embedding_dimensions,omitempty"`
}

type gatewayProviderUpdate struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Kind        string `json:"kind,omitempty"`
	Prefix      string `json:"prefix,omitempty"`
	Enabled     bool   `json:"enabled"`
	BaseURL     string `json:"base_url,omitempty"`
	APIKeyEnv   string `json:"api_key_env,omitempty"`
	APIKey      string `json:"api_key,omitempty"`
	ClearAPIKey bool   `json:"clear_api_key,omitempty"`
}

type modelCatalog struct {
	Models []modelOption `json:"models"`
}

type modelOption struct {
	ID               string   `json:"id"`
	ContextLength    int64    `json:"context_length,omitempty"`
	ReasoningControl string   `json:"reasoning_control,omitempty"`
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
	DefaultEffort    string   `json:"default_effort,omitempty"`
	Group            bool     `json:"group,omitempty"`
}

type apiError struct {
	Error string `json:"error"`
}

func newSettingsService(store config.Store) *settingsService {
	return &settingsService{
		main:      store,
		gateway:   gatewayconfig.Store{Dir: store.Dir},
		remote:    remoteconfig.Store{Dir: store.Dir},
		systemOne: systemoneconfig.Store{Dir: store.Dir},
		mcp:       mcpconfig.Store{Dir: store.Dir},
		providers: providerhost.Store{Dir: store.Dir},
	}
}

func (service *settingsService) serveSnapshot(writer http.ResponseWriter, _ *http.Request) {
	service.mu.Lock()
	defer service.mu.Unlock()
	snapshot, err := service.snapshot()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, snapshot)
}

func (service *settingsService) serveRuntimeUpdate(writer http.ResponseWriter, request *http.Request) {
	var update runtimeUpdate
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if errors.Is(err, config.ErrNotFound) {
		writeAPIError(writer, http.StatusConflict, errors.New("main q configuration is not initialized"))
		return
	}
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	value.Agents.MaxParallel = update.MaxParallel
	value.Context = config.ContextConfig{
		Window: update.Context.Window, TriggerRatio: update.Context.TriggerRatio,
		TargetRatio: update.Context.TargetRatio, RecentRatio: update.Context.RecentRatio,
	}
	value.Loom = config.LoomConfig{
		MaximumArtifactMiB: update.Loom.MaximumArtifactMiB,
		MaximumStoreMiB:    update.Loom.MaximumStoreMiB,
		GC: config.LoomGCConfig{
			Disabled: update.Loom.GCDisabled, TriggerRatio: update.Loom.GCTriggerRatio,
			TargetRatio: update.Loom.GCTargetRatio, GraceHours: update.Loom.GCGraceHours,
		},
	}
	if err := service.main.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveModelCatalog(writer http.ResponseWriter, request *http.Request) {
	service.mu.Lock()
	providerConfig, err := service.providers.Load()
	mainConfig, mainErr := service.main.Load()
	service.mu.Unlock()
	if errors.Is(err, providerhost.ErrNotFound) {
		writeAPIError(writer, http.StatusConflict, errors.New("Gateway providers are not configured"))
		return
	}
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if mainErr != nil {
		writeAPIError(writer, http.StatusInternalServerError, mainErr)
		return
	}
	runtime, err := gateway.NewContext(request.Context(), providerConfig)
	if err != nil {
		writeAPIError(writer, http.StatusBadGateway, fmt.Errorf("initialize Gateway model discovery: %w", err))
		return
	}
	defer func() { _ = runtime.Close() }()
	models, err := runtime.Models(request.Context())
	if err != nil {
		writeAPIError(writer, http.StatusBadGateway, fmt.Errorf("discover Gateway models: %w", err))
		return
	}
	result := modelCatalog{Models: make([]modelOption, 0, len(models)+len(mainConfig.ModelGroups))}
	for _, model := range models {
		option := modelOption{ID: model.ID, ContextLength: model.ContextLength}
		if model.Capabilities != nil && model.Capabilities.Reasoning != nil {
			reasoning := model.Capabilities.Reasoning
			option.ReasoningControl = string(reasoning.Control)
			option.ReasoningEfforts = append([]string(nil), reasoning.SupportedEfforts...)
			option.DefaultEffort = reasoning.DefaultEffort
		}
		result.Models = append(result.Models, option)
	}
	for name := range mainConfig.ModelGroups {
		result.Models = append(result.Models, modelOption{ID: "group/" + name, Group: true})
	}
	sort.Slice(result.Models, func(i, j int) bool { return result.Models[i].ID < result.Models[j].ID })
	writeJSON(writer, http.StatusOK, result)
}

func (service *settingsService) serveModelAssignmentUpdate(writer http.ResponseWriter, request *http.Request) {
	var update modelAssignmentUpdate
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	update.Model = strings.TrimSpace(update.Model)
	update.ReasoningEffort = strings.TrimSpace(update.ReasoningEffort)
	target := request.PathValue("target")
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if errors.Is(err, config.ErrNotFound) {
		writeAPIError(writer, http.StatusConflict, errors.New("main q configuration is not initialized"))
		return
	}
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if update.Model != "" {
		if name, grouped := strings.CutPrefix(update.Model, "group/"); grouped {
			if _, exists := value.ModelGroups[name]; !exists {
				writeAPIError(writer, http.StatusUnprocessableEntity, fmt.Errorf("unknown model group %q", name))
				return
			}
		}
	}
	switch target {
	case "default":
		if update.Model == "" {
			writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("default model is required"))
			return
		}
		value.Provider.Model = update.Model
		value.Provider.ReasoningEffort = update.ReasoningEffort
	case "embedding":
		value.Embedding.Model = update.Model
		value.Embedding.Dimensions = update.EmbeddingDimensions
		if update.Model == "" {
			value.Embedding.Dimensions = 0
		}
	default:
		if !value.HasNativeRole(target) && !config.IsAgentRole(target) {
			writeAPIError(writer, http.StatusNotFound, fmt.Errorf("unknown model role %q", target))
			return
		}
		if value.Agents.Roles == nil {
			value.Agents.Roles = make(map[string]config.AgentConfig)
		}
		assignment := config.AgentConfig{ReasoningEffort: update.ReasoningEffort}
		if group, grouped := strings.CutPrefix(update.Model, "group/"); grouped {
			assignment.Group = group
		} else {
			assignment.Model = update.Model
		}
		if assignment.Model == "" && assignment.Group == "" && assignment.ReasoningEffort == "" {
			delete(value.Agents.Roles, target)
		} else {
			value.Agents.Roles[target] = assignment
		}
	}
	if err := service.main.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveProviderCreate(writer http.ResponseWriter, request *http.Request) {
	var update gatewayProviderUpdate
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.loadProviderConfig()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	provider := applyProviderUpdate(gateway.ProviderConfig{}, update)
	value.Providers = append(value.Providers, provider)
	if err := service.saveProviderConfig(request, value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveProviderUpdate(writer http.ResponseWriter, request *http.Request) {
	var update gatewayProviderUpdate
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.loadProviderConfig()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	index := providerIndex(value, request.PathValue("provider"))
	if index < 0 {
		writeAPIError(writer, http.StatusNotFound, errors.New("Gateway provider does not exist"))
		return
	}
	value.Providers[index] = applyProviderUpdate(value.Providers[index], update)
	if err := service.saveProviderConfig(request, value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveProviderDelete(writer http.ResponseWriter, request *http.Request) {
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.loadProviderConfig()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	index := providerIndex(value, request.PathValue("provider"))
	if index < 0 {
		writeAPIError(writer, http.StatusNotFound, errors.New("Gateway provider does not exist"))
		return
	}
	if len(value.Providers) == 1 {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("at least one Gateway provider is required"))
		return
	}
	value.Providers = append(value.Providers[:index], value.Providers[index+1:]...)
	if err := service.saveProviderConfig(request, value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveServiceUpdate(writer http.ResponseWriter, request *http.Request) {
	var update serviceUpdate
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	update.Host = strings.TrimSpace(update.Host)
	service.mu.Lock()
	defer service.mu.Unlock()
	var err error
	switch request.PathValue("service") {
	case "gateway":
		var value gatewayconfig.Config
		value, err = service.gateway.LoadOrDefault()
		if err == nil {
			value.Server = gatewayconfig.ServerConfig{Host: update.Host, Port: update.Port}
			err = service.gateway.Save(value)
		}
	case "system-one":
		var value systemoneconfig.Config
		value, err = service.systemOne.LoadOrDefault()
		if err == nil {
			value.Server.Host, value.Server.Port = update.Host, update.Port
			err = service.systemOne.Save(value)
		}
	case "remote":
		var value remoteconfig.Config
		value, err = service.remote.LoadOrDefault()
		if err == nil {
			value.Server = remoteconfig.ServerConfig{Host: update.Host, Port: update.Port}
			value.Authentication.Enabled = update.AuthenticationEnabled
			err = service.remote.Save(value)
		}
	default:
		writeAPIError(writer, http.StatusNotFound, errors.New("unknown service settings"))
		return
	}
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) writeSnapshot(writer http.ResponseWriter) {
	snapshot, err := service.snapshot()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, snapshot)
}

func (service *settingsService) snapshot() (settingsSnapshot, error) {
	main, err := service.main.Load()
	configured := true
	if errors.Is(err, config.ErrNotFound) {
		main, configured, err = config.Default(), false, nil
	}
	if err != nil {
		return settingsSnapshot{}, err
	}
	gateway, err := service.gateway.LoadOrDefault()
	if err != nil {
		return settingsSnapshot{}, err
	}
	remote, err := service.remote.LoadOrDefault()
	if err != nil {
		return settingsSnapshot{}, err
	}
	systemOne, err := service.systemOne.LoadOrDefault()
	if err != nil {
		return settingsSnapshot{}, err
	}
	mcp, err := service.mcp.LoadOrDefault()
	if err != nil {
		return settingsSnapshot{}, err
	}
	providerConfig, err := service.loadProviderConfig()
	if err != nil {
		return settingsSnapshot{}, err
	}

	contextValue := main.EffectiveContext()
	loomValue := main.EffectiveLoom()
	agentsValue := main.EffectiveAgents()
	roleAssignments := make([]roleModelAssignment, 0, len(main.NativeRoles()))
	for _, role := range main.NativeRoles() {
		raw, configured := agentsValue.Roles[role]
		configuredModel := raw.Model
		if raw.Group != "" {
			configuredModel = "group/" + raw.Group
		}
		effective, effectiveErr := main.EffectiveAgent(role)
		if effectiveErr != nil {
			return settingsSnapshot{}, effectiveErr
		}
		effectiveModel := effective.Model
		if effective.Group != "" {
			effectiveModel = "group/" + effective.Group
		}
		roleAssignments = append(roleAssignments, roleModelAssignment{
			Role: role, ConfiguredModel: configuredModel, EffectiveModel: effectiveModel,
			ReasoningEffort: raw.ReasoningEffort,
			Inherited:       !configured || raw.Model == "" && raw.Group == "",
		})
	}
	providerItems := make([]gatewayProviderSettings, 0, len(providerConfig.Providers))
	for _, provider := range providerConfig.Providers {
		modelCount := len(provider.Models)
		if len(provider.ModelMetadata) > modelCount {
			modelCount = len(provider.ModelMetadata)
		}
		providerItems = append(providerItems, gatewayProviderSettings{
			ID: provider.ID, Type: provider.Type, Kind: provider.Kind, Prefix: provider.Prefix,
			Enabled: provider.Enabled, BaseURL: provider.BaseURL, APIKeyEnv: provider.APIKeyEnv,
			HasInlineAPIKey: provider.APIKey != "", ModelCount: modelCount,
		})
	}
	return settingsSnapshot{
		Version: 1,
		Scope:   "global",
		Runtime: runtimeSettings{
			Configured: configured, ConfigPath: service.main.Path(), MaxParallel: agentsValue.MaxParallel,
			Context: contextSettings{
				Window: contextValue.Window, TriggerRatio: contextValue.TriggerRatio,
				TargetRatio: contextValue.TargetRatio, RecentRatio: contextValue.RecentRatio,
			},
			Loom: loomSettings{
				MaximumArtifactMiB: loomValue.MaximumArtifactMiB,
				MaximumStoreMiB:    loomValue.MaximumStoreMiB,
				GCDisabled:         loomValue.GC.Disabled,
				GCTriggerRatio:     loomValue.GC.TriggerRatio,
				GCTargetRatio:      loomValue.GC.TargetRatio,
				GCGraceHours:       loomValue.GC.GraceHours,
			},
		},
		Models: modelSettings{
			ConfigPath: service.main.Path(), DefaultModel: main.Provider.Model,
			DefaultReasoning: main.Provider.EffectiveReasoningEffort(),
			EmbeddingModel:   main.Embedding.Model, EmbeddingDimensions: main.Embedding.Dimensions,
			GroupCount: len(main.ModelGroups), Roles: roleAssignments,
		},
		Providers: providerSettings{ConfigPath: service.providers.Path(), Items: providerItems},
		Services: serviceSettings{
			Gateway: listenerSettings{
				ConfigPath: service.gateway.Path(), Host: gateway.Server.Host,
				Port: gateway.Server.Port, ActiveAPIKeys: gateway.ActiveKeyCount(),
			},
			SystemOne: systemOneSettings{
				listenerSettings: listenerSettings{
					ConfigPath: service.systemOne.Path(), Host: systemOne.Server.Host,
					Port: systemOne.Server.Port, ActiveAPIKeys: activeSystemOneKeys(systemOne),
				},
				ProviderCount: len(systemOne.Providers), DefaultModel: systemOne.DefaultModel,
				RoleModelCount: len(systemOne.RoleModels),
			},
			Remote: remoteSettings{
				listenerSettings: listenerSettings{
					ConfigPath: service.remote.Path(), Host: remote.Server.Host,
					Port: remote.Server.Port, ActiveAPIKeys: remote.ActiveKeyCount(),
				},
				AuthenticationEnabled: remote.Authentication.Enabled,
			},
		},
		Integrations: integrationSettings{
			MCP: integrationSummary{ConfigPath: service.mcp.Path(), Items: len(mcp.Servers), Bindings: len(mcp.Roles)},
			LSP: integrationSummary{ConfigPath: service.main.Path(), Items: len(main.LSP.Servers), Bindings: len(main.LSP.Languages)},
		},
	}, nil
}

func (service *settingsService) loadProviderConfig() (gateway.Config, error) {
	value, err := service.providers.Load()
	if errors.Is(err, providerhost.ErrNotFound) {
		return gateway.Config{Providers: make([]gateway.ProviderConfig, 0)}, nil
	}
	return value, err
}

func (service *settingsService) saveProviderConfig(request *http.Request, value gateway.Config) error {
	if err := validateProviderIdentities(value); err != nil {
		return err
	}
	runtime, err := gateway.NewContext(request.Context(), value)
	if err != nil {
		return err
	}
	if err := runtime.Close(); err != nil {
		return fmt.Errorf("close Gateway validation runtime: %w", err)
	}
	return service.providers.Save(value)
}

func validateProviderIdentities(value gateway.Config) error {
	if len(value.Providers) == 0 {
		return errors.New("at least one Gateway provider is required")
	}
	ids := make(map[string]struct{}, len(value.Providers))
	prefixes := make(map[string]struct{}, len(value.Providers))
	for _, provider := range value.Providers {
		if provider.ID == "" || provider.ID != strings.TrimSpace(provider.ID) || strings.ContainsAny(provider.ID, "/\r\n\t ") {
			return fmt.Errorf("Gateway provider ID %q must contain no slash or whitespace", provider.ID)
		}
		if _, duplicate := ids[provider.ID]; duplicate {
			return fmt.Errorf("Gateway provider ID %q is already in use", provider.ID)
		}
		ids[provider.ID] = struct{}{}
		prefix := provider.Prefix
		if prefix == "" {
			prefix = provider.ID
		}
		if prefix != strings.TrimSpace(prefix) || strings.ContainsAny(prefix, "/\r\n\t ") {
			return fmt.Errorf("Gateway provider prefix %q must contain no slash or whitespace", prefix)
		}
		if _, duplicate := prefixes[prefix]; duplicate {
			return fmt.Errorf("Gateway provider prefix %q is already in use", prefix)
		}
		prefixes[prefix] = struct{}{}
	}
	return nil
}

func applyProviderUpdate(provider gateway.ProviderConfig, update gatewayProviderUpdate) gateway.ProviderConfig {
	provider.ID = strings.TrimSpace(update.ID)
	provider.Type = strings.TrimSpace(update.Type)
	provider.Kind = strings.TrimSpace(update.Kind)
	provider.Prefix = strings.TrimSpace(update.Prefix)
	provider.Enabled = update.Enabled
	provider.BaseURL = strings.TrimSpace(update.BaseURL)
	provider.APIKeyEnv = strings.TrimSpace(update.APIKeyEnv)
	if update.ClearAPIKey {
		provider.APIKey = ""
	} else if update.APIKey != "" {
		provider.APIKey = update.APIKey
	}
	return provider
}

func providerIndex(value gateway.Config, id string) int {
	for index, provider := range value.Providers {
		if provider.ID == id {
			return index
		}
	}
	return -1
}

func activeSystemOneKeys(value systemoneconfig.Config) int {
	active := 0
	for _, key := range value.APIKeys {
		if key.RevokedAt == nil {
			active++
		}
	}
	return active
}

func decodeSettingsRequest(writer http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maximumSettingsRequestSize)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode settings: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode settings: multiple JSON values")
		}
		return fmt.Errorf("decode settings: %w", err)
	}
	return nil
}

func writeAPIError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, apiError{Error: err.Error()})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
