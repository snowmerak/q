package studio

import (
	"errors"
	"net/http"
	"strings"

	"github.com/snowmerak/q/config"
)

func (service *settingsService) serveModelOutputLimitUpdate(writer http.ResponseWriter, request *http.Request) {
	var update struct {
		AllowMissingFinishReason *bool  `json:"allow_missing_finish_reason"`
		Model                    string `json:"model"`
		Limit                    *int   `json:"output_limit"`
		Field                    string `json:"output_token_field"`
	}
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if update.Model == "" || strings.TrimSpace(update.Model) != update.Model || strings.HasPrefix(update.Model, "group/") || update.Limit != nil && *update.Limit <= 0 || update.Field != "" && update.Field != "max_tokens" && update.Field != "max_completion_tokens" {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("a concrete model, positive output limit or null, and valid Chat token field are required"))
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusConflict, err)
		return
	}
	providers, err := service.providers.Load()
	if err != nil {
		writeAPIError(writer, http.StatusConflict, err)
		return
	}
	index, upstream, found := studioGatewayModelLocation(providers, update.Model)
	if !found {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("model provider is not configured"))
		return
	}
	provider := providers.Providers[index]
	if update.Limit != nil && (provider.Type == "chatgpt" || provider.Type == "codex") {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("this agent connection does not support an output limit"))
		return
	}
	maximum := provider.ModelMetadata[upstream].MaxOutputTokens
	if update.Limit != nil {
		if runtime, ok := service.runtime.(gatewayManagementRuntime); ok {
			models, err := managedModels(request.Context(), runtime)
			if err != nil {
				writeAPIError(writer, http.StatusBadGateway, err)
				return
			}
			for _, model := range models {
				if model.ID == update.Model {
					maximum = model.MaxOutputTokens
					break
				}
			}
		}
		if maximum > 0 && int64(*update.Limit) > maximum {
			writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("output limit exceeds the model's advertised maximum"))
			return
		}
	}
	if value.ModelOutputLimits == nil {
		value.ModelOutputLimits = make(map[string]int)
	}
	if value.ModelOutputTokenFields == nil {
		value.ModelOutputTokenFields = make(map[string]string)
	}
	if update.Limit == nil {
		delete(value.ModelOutputLimits, update.Model)
	} else {
		value.ModelOutputLimits[update.Model] = *update.Limit
	}
	if update.Field == "" {
		delete(value.ModelOutputTokenFields, update.Model)
	} else {
		value.ModelOutputTokenFields[update.Model] = update.Field
	}
	if update.AllowMissingFinishReason != nil {
		if value.ModelAllowMissingFinishReason == nil {
			value.ModelAllowMissingFinishReason = make(map[string]bool)
		}
		if *update.AllowMissingFinishReason {
			value.ModelAllowMissingFinishReason[update.Model] = true
		} else {
			delete(value.ModelAllowMissingFinishReason, update.Model)
		}
	}
	if err := service.main.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}

func (service *settingsService) serveGenerationRecoveryUpdate(writer http.ResponseWriter, request *http.Request) {
	var update config.GenerationRecoveryConfig
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusConflict, err)
		return
	}
	value.GenerationRecovery = update
	if err := service.main.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}
