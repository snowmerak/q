package studio

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/providerhost"
)

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
	runtime, err := gateway.NewContext(request.Context(), providerhost.LocalConfig(value, service.main.Dir))
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
	if provider.Type == "chatgpt" {
		provider.BaseURL, provider.APIKey, provider.APIKeyEnv = "", "", ""
		return provider
	}
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
