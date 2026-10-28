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
		ACPModel(context.Context, workspace.Store, string, *string) (app.ACPModelSettings, error)
	})
	if !ok {
		writeAPIError(writer, http.StatusServiceUnavailable, app.ErrSessionRuntimeUnavailable)
		return
	}
	var input struct {
		WorkspaceRoot string  `json:"workspace_root"`
		Model         *string `json:"model"`
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
	if request.Method == http.MethodPut && input.Model == nil {
		writeAPIError(writer, http.StatusBadRequest, errors.New("model is required"))
		return
	}
	if request.Method != http.MethodPut {
		input.Model = nil
	}
	ctx, cancel := context.WithTimeout(request.Context(), 3*time.Minute)
	defer cancel()
	result, err := runtime.ACPModel(ctx, workspace.Store{Root: root}, request.PathValue("session"), input.Model)
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
