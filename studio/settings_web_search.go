package studio

import (
	"errors"
	"net/http"
	"strings"

	"github.com/snowmerak/q/config"
)

type webSearchSettings struct {
	config.WebSearchConfig
	NavigationModel string `json:"navigation_model"`
	CompletionModel string `json:"completion_model"`
	Enabled         bool   `json:"enabled"`
}

func (service *settingsService) serveWebSearchUpdate(w http.ResponseWriter, r *http.Request) {
	var update config.WebSearchConfig
	if err := decodeSettingsRequest(w, r, &update); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	update.GenerativeModel = strings.TrimSpace(update.GenerativeModel)
	if update.MaxConcurrentReads < 1 || update.MaxConcurrentReads > 8 {
		writeAPIError(w, http.StatusUnprocessableEntity, errors.New("web_search: concurrent reads must be between 1 and 8"))
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(w, http.StatusConflict, err)
		return
	}
	value.WebSearch = update
	if err := service.main.Save(value); err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(w)
}
