package studio

import (
	"context"
	"net/http"
	"os/exec"
	"time"

	"github.com/snowmerak/q/internal/acpadapter"
)

func (service *integrationService) serveACPAdapters(writer http.ResponseWriter, request *http.Request) {
	manager, err := acpadapter.DefaultManager()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if request.Method == http.MethodGet {
		statuses := make([]acpadapter.Status, 0, 3)
		for _, preset := range []string{"codex", "claude", "grok"} {
			statuses = append(statuses, manager.Status(preset, exec.LookPath))
		}
		writeJSON(writer, http.StatusOK, map[string]any{"adapters": statuses})
		return
	}
	var input struct {
		Preset string `json:"preset"`
	}
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 3*time.Minute)
	defer cancel()
	status, err := manager.Install(ctx, input.Preset)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(writer, http.StatusOK, status)
}
