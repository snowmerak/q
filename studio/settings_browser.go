package studio

import (
	"errors"
	"net/http"
	"sort"

	"github.com/snowmerak/q/browsercontrol"
	"github.com/snowmerak/q/config"
)

func browserSettings(value config.BrowserControlConfig) browserControlSettings {
	value = value.Effective()
	out := browserControlSettings{Config: value, Drivers: []browsercontrol.DriverStatus{}, Prompt: browsercontrol.Prompt(value)}
	for id, driver := range value.Drivers {
		out.Drivers = append(out.Drivers, browsercontrol.Inspect(id, driver))
	}
	sort.Slice(out.Drivers, func(i, j int) bool { return out.Drivers[i].ID < out.Drivers[j].ID })
	return out
}

func (service *settingsService) serveBrowserControlUpdate(writer http.ResponseWriter, request *http.Request) {
	var update config.BrowserControlConfig
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if err := update.Validate(); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if errors.Is(err, config.ErrNotFound) {
		value, err = config.Default(), nil
		value.UseManagedGateway()
	}
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	value.BrowserControl = update
	if err := service.main.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeSnapshot(writer)
}
