package studio

import (
	"errors"
	"net/http"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

type sessionModelUpdate struct {
	WorkspaceRoot   string  `json:"workspace_root"`
	Model           string  `json:"model"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
}

type sessionModelSettings struct {
	Model                    string `json:"model"`
	EffectiveModel           string `json:"effective_model"`
	ReasoningEffort          string `json:"reasoning_effort"`
	EffectiveReasoningEffort string `json:"effective_reasoning_effort"`
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
		candidate := workspace.SessionModelConfig{Model: input.Model}
		if input.ReasoningEffort != nil {
			candidate.ReasoningEffort = *input.ReasoningEffort
		}
		if err := candidate.Validate(); err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		if err := service.updateSessionModel(store, input.Model, input.ReasoningEffort); err != nil {
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
	effort := selected.ReasoningEffort
	if effort == "" {
		effort = value.Provider.EffectiveReasoningEffort()
	}
	writeJSON(writer, http.StatusOK, sessionModelSettings{Model: selected.Model, EffectiveModel: effective,
		ReasoningEffort: selected.ReasoningEffort, EffectiveReasoningEffort: effort})
}

func (service *sessionsService) updateSessionModel(store workspace.Store, model string, effort *string) (returnErr error) {
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
	value, err := store.LoadSessionModelConfig()
	if err != nil {
		return err
	}
	value.Model = model
	// Model-only clients must not silently reset a saved reasoning choice.
	if effort != nil {
		value.ReasoningEffort = *effort
	}
	return store.SaveSessionModelConfig(value)
}
