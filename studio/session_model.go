package studio

import (
	"errors"
	"net/http"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

type sessionModelUpdate struct {
	WorkspaceRoot string `json:"workspace_root"`
	Model         string `json:"model"`
}

type sessionModelSettings struct {
	Model          string `json:"model"`
	EffectiveModel string `json:"effective_model"`
}

func (service *sessionsService) serveSessionModel(writer http.ResponseWriter, request *http.Request) {
	input := sessionModelUpdate{WorkspaceRoot: request.URL.Query().Get("workspace_root")}
	if request.Method == http.MethodPut {
		if err := decodeSessionRequest(writer, request, &input); err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	store, err := (workspace.Store{Root: root}).ForSession(request.PathValue("session"))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if request.Method == http.MethodPut {
		if err := (workspace.SessionModelConfig{Model: input.Model}).Validate(); err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		if err := service.updateSessionModel(store, input.Model); err != nil {
			writeSessionError(writer, err)
			return
		}
	}
	if _, err := store.Load(); err != nil {
		writeSessionError(writer, err)
		return
	}
	selected, err := store.LoadSessionModelConfig()
	if err != nil {
		writeSessionError(writer, err)
		return
	}
	value, err := service.main.Load()
	if errors.Is(err, config.ErrNotFound) {
		value, err = config.Default(), nil
	}
	if err != nil {
		writeSessionError(writer, err)
		return
	}
	local, err := store.LoadModelConfig()
	if err != nil {
		writeSessionError(writer, err)
		return
	}
	effective := selected.Model
	if effective == "" {
		effective = local.Overrides["default"].Model
	}
	if effective == "" {
		effective = value.Provider.Model
	}
	writeJSON(writer, http.StatusOK, sessionModelSettings{Model: selected.Model, EffectiveModel: effective})
}

func (service *sessionsService) updateSessionModel(store workspace.Store, model string) (returnErr error) {
	if err := store.RequireInteraction(); err != nil {
		return err
	}
	// The live runtime owns the session lock between turns. Release it before
	// saving; the shared lock also excludes another Studio or TUI owner.
	if err := service.releaseSession(store.Root, store.SessionID); err != nil {
		return err
	}
	lock, err := workspace.AcquireSessionLock(store.Root, store.SessionID, "q studio session model")
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	if _, err := store.Load(); err != nil {
		return err
	}
	return store.SaveSessionModelConfig(workspace.SessionModelConfig{Model: model})
}
