package studio

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/systemoneconfig"
)

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
	case "agent-skill-decision", "archive-decision":
		role := systemoneconfig.RoleAgentSkillDecision
		if request.PathValue("target") == "archive-decision" {
			role = systemoneconfig.RoleArchiveDecision
		}
		if value.RoleModels == nil {
			value.RoleModels = make(map[string]string)
		}
		if update.Model == "" {
			delete(value.RoleModels, role)
		} else {
			value.RoleModels[role] = update.Model
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
