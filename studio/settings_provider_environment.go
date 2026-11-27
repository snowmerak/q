package studio

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/internal/providerenv"
	"github.com/snowmerak/q/providerhost"
)

// serveProviderEnvironmentReload refreshes the provider credentials that
// Studio inherited at startup. It reads the variables referenced by provider
// settings from the configured source, replaces the managed Gateway with a
// child that receives them, and then tells the standalone Gateway and
// Decisions to rebuild their provider clients. Any failure keeps the working
// credentials. Responses name variables but never include their values.
func (service *settingsService) serveProviderEnvironmentReload(writer http.ResponseWriter, request *http.Request) {
	service.mu.Lock()
	defer service.mu.Unlock()
	mainConfig, err := service.main.Load()
	if errors.Is(err, config.ErrNotFound) {
		mainConfig, err = config.Config{}, nil
	}
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	providers, err := service.loadProviderConfig()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	decisions, err := service.systemOne.LoadOrDefault()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	names := providerhost.EnvironmentNames(providers)
	for _, provider := range decisions.Providers {
		names = append(names, provider.APIKeyEnv)
	}
	names = append(names, mainConfig.Provider.APIKeyEnv)

	settings := providerenv.Settings{
		Shell:   mainConfig.ProviderEnvironment.Shell,
		Command: mainConfig.ProviderEnvironment.Command,
	}
	values, result, err := providerenv.Load(request.Context(), settings, names)
	if errors.Is(err, providerenv.ErrNotConfigured) {
		writeAPIError(writer, http.StatusConflict, errors.New("set provider_environment.command in config.yaml to reload provider credentials"))
		return
	}
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	previous := providerenv.Replace(values)
	if service.runtime != nil && len(providers.Providers) > 0 {
		if err := service.runtime.ApplyGateway(request.Context(), providers); err != nil {
			providerenv.Replace(previous)
			writeAPIError(writer, http.StatusBadGateway, fmt.Errorf("restart Gateway with refreshed credentials: %w", err))
			return
		}
	}
	providerenv.Notify()
	writeJSON(writer, http.StatusOK, result)
}
