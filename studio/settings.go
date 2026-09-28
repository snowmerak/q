package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/gatewayconfig"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/systemoneconfig"
	qtools "github.com/snowmerak/q/tools"
)

const maximumSettingsRequestSize = 64 << 10

type settingsService struct {
	mu        sync.Mutex
	main      config.Store
	gateway   gatewayconfig.Store
	systemOne systemoneconfig.Store
	mcp       mcpconfig.Store
	providers providerhost.Store
	library   qlibrary.ConfigStore
	runtime   settingsRuntime
}

type settingsRuntime interface {
	ApplyGateway(context.Context, gateway.Config) error
}

type embeddingRuntime interface {
	SyncEmbeddings(context.Context, string, config.Config) error
}

type settingsSnapshot struct {
	Version      int                  `json:"version"`
	Scope        string               `json:"scope"`
	Runtime      runtimeSettings      `json:"runtime"`
	Models       modelSettings        `json:"models"`
	Providers    providerSettings     `json:"gateway_providers"`
	SystemOne    systemOneAPISettings `json:"system_one"`
	Services     serviceSettings      `json:"services"`
	Integrations integrationSettings  `json:"integrations"`
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
	Groups              []modelGroupSettings  `json:"groups"`
	Roles               []roleModelAssignment `json:"roles"`
}

type modelGroupSettings struct {
	Name       string                   `json:"name"`
	Candidates []modelCandidateSettings `json:"candidates"`
}

type modelCandidateSettings struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	Timeout         string `json:"timeout,omitempty"`
}

type roleModelAssignment struct {
	Role            string `json:"role"`
	ConfiguredModel string `json:"configured_model"`
	EffectiveModel  string `json:"effective_model"`
	ReasoningEffort string `json:"reasoning_effort"`
	Inherited       bool   `json:"inherited"`
	Custom          bool   `json:"custom"`
}

type providerSettings struct {
	ConfigPath string                    `json:"config_path"`
	Items      []gatewayProviderSettings `json:"items"`
	APIKeys    []serviceAPIKeySettings   `json:"api_keys"`
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

type systemOneAPISettings struct {
	ConfigPath      string                      `json:"config_path"`
	DefaultModel    string                      `json:"default_model"`
	AgentSkillModel string                      `json:"agent_skill_model"`
	Providers       []systemOneProviderSettings `json:"providers"`
	APIKeys         []serviceAPIKeySettings     `json:"api_keys"`
	ActiveAPIKeys   int                         `json:"active_api_keys"`
}

type systemOneProviderSettings struct {
	ID        string `json:"id"`
	URI       string `json:"uri"`
	APIKeyEnv string `json:"api_key_env"`
}

type serviceAPIKeySettings struct {
	ID        string     `json:"id"`
	Alias     string     `json:"alias"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	Legacy    bool       `json:"legacy,omitempty"`
}

type serviceSettings struct {
	Gateway   listenerSettings  `json:"gateway"`
	SystemOne systemOneSettings `json:"system_one"`
	Library   listenerSettings  `json:"library"`
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
	Host string `json:"host"`
	Port int    `json:"port"`
}

type modelAssignmentUpdate struct {
	Model               string `json:"model"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
	EmbeddingDimensions int    `json:"embedding_dimensions,omitempty"`
	WorkspaceRoot       string `json:"workspace_root,omitempty"`
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

type systemOneProviderUpdate struct {
	ID        string `json:"id"`
	URI       string `json:"uri"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
}

type systemOneModelUpdate struct {
	Model string `json:"model"`
}

type apiKeyCreateRequest struct {
	Alias string `json:"alias"`
}

type apiKeyCreateResponse struct {
	Settings settingsSnapshot `json:"settings"`
	Secret   string           `json:"secret"`
}

type modelCatalog struct {
	Models []modelOption `json:"models"`
}

type systemOneModelCatalog struct {
	Models []systemOneModelOption `json:"models"`
}

type systemOneModelOption struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
}

type modelOption struct {
	ID               string   `json:"id"`
	ContextLength    int64    `json:"context_length,omitempty"`
	ReasoningControl string   `json:"reasoning_control,omitempty"`
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
	DefaultEffort    string   `json:"default_effort,omitempty"`
	Group            bool     `json:"group,omitempty"`
	APIMode          string   `json:"api_mode,omitempty"`
	ContextOverride  int64    `json:"context_override,omitempty"`
}

type modelGroupUpdate struct {
	Name       string                   `json:"name"`
	Candidates []modelCandidateSettings `json:"candidates"`
}

type customRoleUpdate struct {
	Role string `json:"role"`
}

type modelAPIModeUpdate struct {
	Model string `json:"model"`
	Mode  string `json:"mode"`
}

type modelMetadataUpdate struct {
	Model         string `json:"model"`
	ContextWindow int64  `json:"context_window"`
}

type loomOperationRequest struct {
	WorkspaceRoot string `json:"workspace_root"`
	DryRun        bool   `json:"dry_run"`
}

type loomStatusResponse struct {
	WorkspaceRoot string         `json:"workspace_root"`
	Stats         loom.Stats     `json:"stats"`
	Result        *loom.GCResult `json:"result,omitempty"`
}

type apiError struct {
	Error string `json:"error"`
}

func newSettingsService(store config.Store, runtimes ...settingsRuntime) *settingsService {
	service := &settingsService{
		main:      store,
		gateway:   gatewayconfig.Store{Dir: store.Dir},
		systemOne: systemoneconfig.Store{Dir: store.Dir},
		mcp:       mcpconfig.Store{Dir: store.Dir},
		providers: providerhost.Store{Dir: store.Dir},
		library:   qlibrary.ConfigStore{Dir: store.Dir},
	}
	if len(runtimes) > 0 {
		service.runtime = runtimes[0]
	}
	return service
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

func (service *settingsService) serveLoomStatus(writer http.ResponseWriter, request *http.Request) {
	root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	runtime, err := service.openLoomRuntime(request.Context(), root)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = runtime.Close() }()
	stats, err := runtime.LoomStats(request.Context())
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, loomStatusResponse{WorkspaceRoot: root, Stats: stats})
}

func (service *settingsService) serveLoomCollect(writer http.ResponseWriter, request *http.Request) {
	var input loomOperationRequest
	if err := decodeSettingsRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	runtime, err := service.openLoomRuntime(request.Context(), root)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = runtime.Close() }()
	result, err := runtime.CollectLoom(request.Context(), input.DryRun)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	stats, err := runtime.LoomStats(request.Context())
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, loomStatusResponse{WorkspaceRoot: root, Stats: stats, Result: &result})
}

func (service *settingsService) openLoomRuntime(ctx context.Context, root string) (*qtools.Runtime, error) {
	service.mu.Lock()
	value, err := service.main.Load()
	service.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return qtools.NewRuntimeWithArchiveAndLoomOptions(ctx, root, nil, value.LoomStoreOptions(nil))
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
		option := modelOption{ID: model.ID, ContextLength: model.ContextLength, APIMode: mainConfig.ModelAPIMode(model.ID)}
		if providerIndex, upstreamID, found := studioGatewayModelLocation(providerConfig, model.ID); found {
			option.ContextOverride = providerConfig.Providers[providerIndex].ModelMetadata[upstreamID].ContextLength
		}
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

func (service *settingsService) serveModelGroupUpsert(writer http.ResponseWriter, request *http.Request) {
	var update modelGroupUpdate
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	update.Name = strings.TrimSpace(update.Name)
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if value.ModelGroups == nil {
		value.ModelGroups = make(map[string]config.ModelGroupConfig)
	}
	candidates := make([]config.ModelCandidateConfig, 0, len(update.Candidates))
	for _, candidate := range update.Candidates {
		timeout := time.Duration(0)
		if strings.TrimSpace(candidate.Timeout) != "" {
			timeout, err = time.ParseDuration(strings.TrimSpace(candidate.Timeout))
			if err != nil {
				writeAPIError(writer, http.StatusUnprocessableEntity, fmt.Errorf("invalid candidate timeout: %w", err))
				return
			}
		}
		candidates = append(candidates, config.ModelCandidateConfig{
			Model: strings.TrimSpace(candidate.Model), ReasoningEffort: strings.TrimSpace(candidate.ReasoningEffort), Timeout: timeout,
		})
	}
	value.ModelGroups[update.Name] = config.ModelGroupConfig{Candidates: candidates}
	if err := service.main.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveModelGroupDelete(writer http.ResponseWriter, request *http.Request) {
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	name := request.PathValue("group")
	if _, found := value.ModelGroups[name]; !found {
		writeAPIError(writer, http.StatusNotFound, errors.New("model group does not exist"))
		return
	}
	delete(value.ModelGroups, name)
	if err := service.main.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveCustomRoleCreate(writer http.ResponseWriter, request *http.Request) {
	var update customRoleUpdate
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	update.Role = strings.TrimSpace(update.Role)
	if !config.ValidCustomRoleName(update.Role) {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("custom role must start with a lowercase letter and use lowercase letters, digits, or hyphens"))
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if value.Agents.Roles == nil {
		value.Agents.Roles = make(map[string]config.AgentConfig)
	}
	if _, exists := value.Agents.Roles[update.Role]; exists {
		writeAPIError(writer, http.StatusConflict, errors.New("custom role already exists"))
		return
	}
	value.Agents.Roles[update.Role] = config.AgentConfig{}
	if err := service.main.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveCustomRoleDelete(writer http.ResponseWriter, request *http.Request) {
	role := request.PathValue("role")
	if !config.ValidCustomRoleName(role) {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("only custom roles can be deleted"))
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if !value.HasNativeRole(role) {
		writeAPIError(writer, http.StatusNotFound, errors.New("custom role does not exist"))
		return
	}
	root := ""
	if requestedRoot := strings.TrimSpace(request.URL.Query().Get("workspace_root")); requestedRoot != "" {
		root, err = canonicalWorkspaceDirectory(requestedRoot)
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
	}
	var references []string
	for _, entry := range profileStore(service.main, root).List() {
		if entry.Err == nil && entry.Profile.EffectiveKind() == "inner" && entry.Profile.Role == role {
			references = append(references, entry.Scope+"/"+entry.Profile.Name)
		}
	}
	if len(references) > 0 {
		sort.Strings(references)
		writeAPIError(writer, http.StatusConflict, fmt.Errorf("custom role is used by subagents: %s", strings.Join(references, ", ")))
		return
	}
	delete(value.Agents.Roles, role)
	if err := service.main.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveModelAPIModeUpdate(writer http.ResponseWriter, request *http.Request) {
	var update modelAPIModeUpdate
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	update.Model, update.Mode = strings.TrimSpace(update.Model), strings.TrimSpace(update.Mode)
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if value.ModelAPIModes == nil {
		value.ModelAPIModes = make(map[string]string)
	}
	if update.Mode == "" || update.Mode == "chat_completions" {
		delete(value.ModelAPIModes, update.Model)
	} else {
		value.ModelAPIModes[update.Model] = update.Mode
	}
	if err := service.main.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveModelMetadataUpdate(writer http.ResponseWriter, request *http.Request) {
	var update modelMetadataUpdate
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	update.Model = strings.TrimSpace(update.Model)
	if update.ContextWindow < 0 {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("context window must not be negative"))
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.loadProviderConfig()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	providerIndex, upstreamID, found := studioGatewayModelLocation(value, update.Model)
	if !found {
		writeAPIError(writer, http.StatusNotFound, errors.New("model is not mapped to a Gateway provider"))
		return
	}
	provider := &value.Providers[providerIndex]
	if provider.ModelMetadata == nil {
		provider.ModelMetadata = make(map[string]client.ModelMetadata)
	}
	metadata := provider.ModelMetadata[upstreamID]
	metadata.ContextLength = update.ContextWindow
	if metadata.ContextLength == 0 && metadata.MaxOutputTokens == 0 && metadata.Capabilities == nil {
		delete(provider.ModelMetadata, upstreamID)
	} else {
		provider.ModelMetadata[upstreamID] = metadata
	}
	if err := service.saveProviderConfig(request, value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
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
	if target == "embedding" {
		if runtime, ok := service.runtime.(embeddingRuntime); ok {
			root := strings.TrimSpace(update.WorkspaceRoot)
			if root != "" {
				root, err = canonicalWorkspaceDirectory(root)
				if err != nil {
					writeAPIError(writer, http.StatusBadRequest, err)
					return
				}
			}
			if err := runtime.SyncEmbeddings(request.Context(), root, value); err != nil {
				writeAPIError(writer, http.StatusBadGateway, fmt.Errorf("embedding settings saved, but reindexing failed: %w", err))
				return
			}
		}
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

func (service *settingsService) serveSystemOneModelCatalog(writer http.ResponseWriter, request *http.Request) {
	service.mu.Lock()
	value, err := service.systemOne.LoadOrDefault()
	service.mu.Unlock()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
	defer cancel()
	result := systemOneModelCatalog{Models: make([]systemOneModelOption, 0)}
	var firstError error
	for _, provider := range value.Providers {
		client, clientErr := provider.NewClient()
		if clientErr == nil {
			var catalog *systemone.ModelsResult
			catalog, clientErr = client.ListModels(ctx)
			if clientErr == nil {
				for _, model := range catalog.Models {
					result.Models = append(result.Models, systemOneModelOption{
						ID: provider.ID + "/" + model.Name, Description: model.Description, ReleaseDate: model.ReleaseDate,
					})
				}
			}
		}
		if clientErr != nil && firstError == nil {
			firstError = fmt.Errorf("%s: %w", provider.ID, clientErr)
		}
	}
	if len(result.Models) == 0 && firstError != nil {
		writeAPIError(writer, http.StatusBadGateway, fmt.Errorf("discover System One models: %w", firstError))
		return
	}
	sort.Slice(result.Models, func(i, j int) bool { return result.Models[i].ID < result.Models[j].ID })
	writeJSON(writer, http.StatusOK, result)
}

func (service *settingsService) serveSystemOneModelAssignmentUpdate(writer http.ResponseWriter, request *http.Request) {
	var update systemOneModelUpdate
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	update.Model = strings.TrimSpace(update.Model)
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.systemOne.LoadOrDefault()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	switch request.PathValue("target") {
	case "default":
		if update.Model == "" {
			writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("System One default model is required"))
			return
		}
		value.DefaultModel = update.Model
	case "agent-skill-decision":
		if value.RoleModels == nil {
			value.RoleModels = make(map[string]string)
		}
		if update.Model == "" {
			delete(value.RoleModels, systemoneconfig.RoleAgentSkillDecision)
		} else {
			value.RoleModels[systemoneconfig.RoleAgentSkillDecision] = update.Model
		}
	default:
		writeAPIError(writer, http.StatusNotFound, errors.New("unknown System One model assignment"))
		return
	}
	if err := service.systemOne.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveSystemOneProviderCreate(writer http.ResponseWriter, request *http.Request) {
	var update systemOneProviderUpdate
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.systemOne.LoadOrDefault()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	value.Providers = append(value.Providers, applySystemOneProviderUpdate(systemoneconfig.ProviderConfig{}, update))
	if err := service.systemOne.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveSystemOneProviderUpdate(writer http.ResponseWriter, request *http.Request) {
	var update systemOneProviderUpdate
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.systemOne.LoadOrDefault()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	index := systemOneProviderIndex(value, request.PathValue("provider"))
	if index < 0 {
		writeAPIError(writer, http.StatusNotFound, errors.New("System One provider does not exist"))
		return
	}
	oldID := value.Providers[index].ID
	value.Providers[index] = applySystemOneProviderUpdate(value.Providers[index], update)
	if oldID != value.Providers[index].ID {
		rewriteSystemOneProviderReferences(&value, oldID, value.Providers[index].ID)
	}
	if err := service.systemOne.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveSystemOneProviderDelete(writer http.ResponseWriter, request *http.Request) {
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.systemOne.LoadOrDefault()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	index := systemOneProviderIndex(value, request.PathValue("provider"))
	if index < 0 {
		writeAPIError(writer, http.StatusNotFound, errors.New("System One provider does not exist"))
		return
	}
	if len(value.Providers) == 1 {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("at least one System One provider is required"))
		return
	}
	value.Providers = append(value.Providers[:index], value.Providers[index+1:]...)
	if err := service.systemOne.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveSystemOneAPIKeyCreate(writer http.ResponseWriter, request *http.Request) {
	var update apiKeyCreateRequest
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.systemOne.LoadOrDefault()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	_, generated, err := service.systemOne.CreateAPIKey(value, update.Alias, time.Now())
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	snapshot, err := service.snapshot()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusCreated, apiKeyCreateResponse{Settings: snapshot, Secret: generated.Secret})
}

func (service *settingsService) serveSystemOneAPIKeyRevoke(writer http.ResponseWriter, request *http.Request) {
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.systemOne.LoadOrDefault()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if _, err := service.systemOne.RevokeAPIKey(value, request.PathValue("key"), time.Now()); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveGatewayAPIKeyCreate(writer http.ResponseWriter, request *http.Request) {
	var update apiKeyCreateRequest
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.gateway.LoadOrDefault()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	_, generated, err := service.gateway.CreateAPIKey(value, update.Alias, time.Now())
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	snapshot, err := service.snapshot()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusCreated, apiKeyCreateResponse{Settings: snapshot, Secret: generated.Secret})
}

func (service *settingsService) serveGatewayAPIKeyRevoke(writer http.ResponseWriter, request *http.Request) {
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.gateway.LoadOrDefault()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if _, err := service.gateway.RevokeAPIKey(value, request.PathValue("key"), time.Now()); err != nil {
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
	case "library":
		var value qlibrary.Config
		value, err = service.library.LoadOrDefault()
		if err == nil {
			value.Host, value.Port = update.Host, update.Port
			err = service.library.Save(value)
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
	systemOne, err := service.systemOne.LoadOrDefault()
	if err != nil {
		return settingsSnapshot{}, err
	}
	library, err := service.library.LoadOrDefault()
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
			Custom:          config.ValidCustomRoleName(role),
		})
	}
	groups := make([]modelGroupSettings, 0, len(main.ModelGroups))
	for name, group := range main.ModelGroups {
		item := modelGroupSettings{Name: name, Candidates: make([]modelCandidateSettings, 0, len(group.Candidates))}
		for _, candidate := range group.Candidates {
			timeout := ""
			if candidate.Timeout > 0 {
				timeout = candidate.Timeout.String()
			}
			item.Candidates = append(item.Candidates, modelCandidateSettings{
				Model: candidate.Model, ReasoningEffort: candidate.ReasoningEffort, Timeout: timeout,
			})
		}
		groups = append(groups, item)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
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
	gatewayKeys := make([]serviceAPIKeySettings, 0, len(gateway.APIKeys))
	for _, key := range gateway.APIKeys {
		createdAt := key.CreatedAt
		gatewayKeys = append(gatewayKeys, serviceAPIKeySettings{
			ID: key.ID, Alias: key.Alias, CreatedAt: &createdAt, RevokedAt: key.RevokedAt,
		})
	}
	systemOneProviders := make([]systemOneProviderSettings, 0, len(systemOne.Providers))
	for _, provider := range systemOne.Providers {
		systemOneProviders = append(systemOneProviders, systemOneProviderSettings{
			ID: provider.ID, URI: provider.URI, APIKeyEnv: provider.APIKeyEnv,
		})
	}
	systemOneKeys := make([]serviceAPIKeySettings, 0, len(systemOne.APIKeys)+1)
	if systemOne.Server.APIKey != "" {
		systemOneKeys = append(systemOneKeys, serviceAPIKeySettings{ID: "legacy", Alias: "Legacy key", Legacy: true})
	}
	for _, key := range systemOne.APIKeys {
		createdAt := key.CreatedAt
		systemOneKeys = append(systemOneKeys, serviceAPIKeySettings{
			ID: key.ID, Alias: key.Alias, CreatedAt: &createdAt, RevokedAt: key.RevokedAt,
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
			GroupCount: len(main.ModelGroups), Groups: groups, Roles: roleAssignments,
		},
		Providers: providerSettings{ConfigPath: service.providers.Path(), Items: providerItems, APIKeys: gatewayKeys},
		SystemOne: systemOneAPISettings{
			ConfigPath: service.systemOne.Path(), DefaultModel: systemOne.DefaultModel,
			AgentSkillModel: systemOne.RoleModels[systemoneconfig.RoleAgentSkillDecision],
			Providers:       systemOneProviders, APIKeys: systemOneKeys, ActiveAPIKeys: systemOne.ActiveKeyCount(),
		},
		Services: serviceSettings{
			Gateway: listenerSettings{
				ConfigPath: service.gateway.Path(), Host: gateway.Server.Host,
				Port: gateway.Server.Port, ActiveAPIKeys: gateway.ActiveKeyCount(),
			},
			SystemOne: systemOneSettings{
				listenerSettings: listenerSettings{
					ConfigPath: service.systemOne.Path(), Host: systemOne.Server.Host,
					Port: systemOne.Server.Port, ActiveAPIKeys: systemOne.ActiveKeyCount(),
				},
				ProviderCount: len(systemOne.Providers), DefaultModel: systemOne.DefaultModel,
				RoleModelCount: len(systemOne.RoleModels),
			},
			Library: listenerSettings{ConfigPath: service.library.Path(), Host: library.Host, Port: library.Port},
		},
		Integrations: integrationSettings{
			MCP: integrationSummary{ConfigPath: service.mcp.Path(), Items: len(mcp.Servers), Bindings: len(mcp.Roles)},
			LSP: integrationSummary{ConfigPath: service.main.Path(), Items: len(main.LSP.Servers), Bindings: len(main.LSP.Languages)},
		},
	}, nil
}

func studioGatewayModelLocation(value gateway.Config, modelID string) (int, string, bool) {
	prefix, upstreamID, found := strings.Cut(modelID, "/")
	if !found || prefix == "" || upstreamID == "" {
		return 0, "", false
	}
	for index, provider := range value.Providers {
		effectivePrefix := provider.Prefix
		if effectivePrefix == "" {
			effectivePrefix = provider.ID
		}
		if effectivePrefix == prefix {
			return index, upstreamID, true
		}
	}
	return 0, "", false
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
	if service.runtime != nil {
		return service.runtime.ApplyGateway(request.Context(), value)
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

func applySystemOneProviderUpdate(provider systemoneconfig.ProviderConfig, update systemOneProviderUpdate) systemoneconfig.ProviderConfig {
	provider.ID = strings.TrimSpace(update.ID)
	provider.URI = strings.TrimSpace(update.URI)
	provider.APIKeyEnv = strings.TrimSpace(update.APIKeyEnv)
	return provider
}

func systemOneProviderIndex(value systemoneconfig.Config, id string) int {
	for index, provider := range value.Providers {
		if provider.ID == id {
			return index
		}
	}
	return -1
}

func rewriteSystemOneProviderReferences(value *systemoneconfig.Config, oldID, newID string) {
	rewrite := func(model string) string {
		if strings.HasPrefix(model, oldID+"/") {
			return newID + strings.TrimPrefix(model, oldID)
		}
		return model
	}
	value.DefaultModel = rewrite(value.DefaultModel)
	for role, model := range value.RoleModels {
		value.RoleModels[role] = rewrite(model)
	}
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
