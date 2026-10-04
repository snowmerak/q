package studio

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	llmprovider "github.com/snowmerak/llm-provider"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/providerhost"
)

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
	var models []llmprovider.Model
	if runtime, ok := service.runtime.(gatewayManagementRuntime); ok {
		models, err = managedModels(request.Context(), runtime)
	} else {
		// Headless test/embedder fallback uses Q's own identity as well. The
		// running Studio always delegates credential ownership to its child.
		var runtime *gateway.Gateway
		runtime, err = gateway.NewContext(request.Context(), providerhost.LocalConfig(providerConfig, service.main.Dir))
		if err == nil {
			defer func() { _ = runtime.Close() }()
			models, err = runtime.Models(request.Context())
		}
	}
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
		if target != "default" {
			writeAPIError(writer, http.StatusConflict, errors.New("choose the default model before configuring other model roles"))
			return
		}
		value, err = config.Default(), nil
		value.UseManagedGateway()
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
