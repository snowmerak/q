package studio

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/workspace"
)

func (service *sessionsService) serveACPModel(writer http.ResponseWriter, request *http.Request) {
	runtime, ok := service.runner.(interface {
		ConfigureACP(context.Context, workspace.Store, string, app.ACPSettingsUpdate) (app.ACPModelSettings, error)
	})
	if !ok {
		writeAPIError(writer, http.StatusServiceUnavailable, app.ErrSessionRuntimeUnavailable)
		return
	}
	var input struct {
		WorkspaceRoot   string  `json:"workspace_root"`
		Model           *string `json:"model"`
		ReasoningEffort *string `json:"reasoning_effort"`
	}
	if err := decodeSessionRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if request.Method == http.MethodPut && input.Model == nil && input.ReasoningEffort == nil {
		writeAPIError(writer, http.StatusBadRequest, errors.New("model or reasoning_effort is required"))
		return
	}
	if request.Method != http.MethodPut {
		input.Model = nil
		input.ReasoningEffort = nil
	}
	ctx, cancel := context.WithTimeout(request.Context(), 3*time.Minute)
	defer cancel()
	result, err := runtime.ConfigureACP(ctx, workspace.Store{Root: root}, request.PathValue("session"), app.ACPSettingsUpdate{Model: input.Model, ReasoningEffort: input.ReasoningEffort})
	if err != nil {
		if errors.Is(err, app.ErrACPModelSelection) {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
		} else {
			writeSessionError(writer, err)
		}
		return
	}
	writeJSON(writer, http.StatusOK, result)
}
