package studio

import (
	"errors"
	"net/http"

	"github.com/snowmerak/q/mcpconfig"
)

type mcpSettingsResponse struct {
	ConfigPath string           `json:"config_path"`
	Config     mcpconfig.Config `json:"config"`
	Roles      []string         `json:"roles"`
}

func (service *integrationService) serveMCP(writer http.ResponseWriter, request *http.Request) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if request.Method == http.MethodGet {
		value, err := service.mcp.LoadOrDefault()
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		writeJSON(writer, http.StatusOK, mcpSettingsResponse{ConfigPath: service.mcp.Path(), Config: value, Roles: mcpconfig.RoleIDs()})
		return
	}
	if request.Method != http.MethodPut {
		writer.Header().Set("Allow", "GET, PUT")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	var value mcpconfig.Config
	if err := decodeIntegrationRequest(writer, request, &value); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if err := service.mcp.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	value, _ = service.mcp.LoadOrDefault()
	writeJSON(writer, http.StatusOK, mcpSettingsResponse{ConfigPath: service.mcp.Path(), Config: value, Roles: mcpconfig.RoleIDs()})
}
