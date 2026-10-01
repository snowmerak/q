package studio

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
)

type delegationController interface {
	DelegationRun(workspace.Store) (app.DelegationRunSnapshot, bool)
	ControlDelegation(context.Context, workspace.Store, string, string, string) error
}

type delegationStarter interface {
	StartDelegation(context.Context, workspace.Store, string, string) (app.DelegationRunSnapshot, error)
}

type delegationSessionResponse struct {
	Node delegationNode             `json:"node"`
	Kind string                     `json:"kind"`
	Run  *app.DelegationRunSnapshot `json:"run,omitempty"`
}

type delegationCommandRequest struct {
	WorkspaceRoot string `json:"workspace_root"`
	Path          string `json:"path"`
	RunID         string `json:"run_id"`
	Action        string `json:"action"`
	Content       string `json:"content,omitempty"`
}

func findDelegation(root, sessionID, path string) (workspace.Store, workspace.DelegationBookmark, error) {
	store, err := (workspace.Store{Root: root}).ForSession(sessionID)
	if err != nil {
		return workspace.Store{}, workspace.DelegationBookmark{}, err
	}
	parts := strings.Split(path, "/")
	if path == "" || len(parts) > maximumDelegationTreeDepth {
		return workspace.Store{}, workspace.DelegationBookmark{}, errors.New("invalid delegation path")
	}
	var selected workspace.DelegationBookmark
	for _, part := range parts {
		items, err := store.LoadDelegations()
		if err != nil {
			return workspace.Store{}, selected, err
		}
		found := false
		for _, item := range items {
			if item.InvocationID == part {
				selected, found = item, true
				break
			}
		}
		if !found {
			return workspace.Store{}, selected, workspace.ErrDelegationNotFound
		}
		store, err = store.ChildStore(part)
		if err != nil {
			return workspace.Store{}, selected, err
		}
	}
	return store, selected, nil
}

func (service *sessionsService) delegationSession(store workspace.Store, bookmark workspace.DelegationBookmark) (delegationSessionResponse, error) {
	state, err := store.LoadDelegationState()
	if err != nil {
		return delegationSessionResponse{}, err
	}
	session, err := store.Load()
	if err != nil {
		return delegationSessionResponse{}, err
	}
	response := delegationSessionResponse{
		Node: delegationNode{Bookmark: bookmark, State: &state, Transcript: detailFromSession(store.Root, store, session).Transcript},
		Kind: app.DelegationKind(state),
	}
	if controller, ok := service.runner.(delegationController); ok {
		if run, found := controller.DelegationRun(store); found {
			response.Run = &run
		}
	}
	return response, nil
}

func (service *sessionsService) serveDelegationSession(writer http.ResponseWriter, request *http.Request) {
	root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	store, bookmark, err := findDelegation(root, request.PathValue("session"), request.URL.Query().Get("path"))
	if err != nil {
		writeAPIError(writer, http.StatusNotFound, err)
		return
	}
	response, err := service.delegationSession(store, bookmark)
	if err != nil {
		writeSessionError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (service *sessionsService) serveDelegationCommand(writer http.ResponseWriter, request *http.Request) {
	var input delegationCommandRequest
	if err := decodeSessionRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	store, bookmark, err := findDelegation(root, request.PathValue("session"), input.Path)
	if err != nil {
		writeAPIError(writer, http.StatusNotFound, err)
		return
	}
	response, err := service.delegationSession(store, bookmark)
	if err != nil {
		writeSessionError(writer, err)
		return
	}
	if response.Kind != subagent.AgentKindInner {
		writeAPIError(writer, http.StatusForbidden, errors.New("external ACP sessions do not support interactive control"))
		return
	}
	if input.Action == "message" {
		starter, ok := service.runner.(delegationStarter)
		if !ok {
			writeAPIError(writer, http.StatusServiceUnavailable, app.ErrSessionRuntimeUnavailable)
			return
		}
		rootStore := workspace.Store{Root: root, SessionID: request.PathValue("session")}
		run, err := starter.StartDelegation(request.Context(), rootStore, input.Path, input.Content)
		if err != nil {
			writeAPIError(writer, http.StatusConflict, err)
			return
		}
		response.Run = &run
		writeJSON(writer, http.StatusAccepted, response)
		return
	}
	controller, ok := service.runner.(delegationController)
	if !ok {
		writeAPIError(writer, http.StatusServiceUnavailable, app.ErrSessionRuntimeUnavailable)
		return
	}
	if err := controller.ControlDelegation(request.Context(), store, input.RunID, input.Action, input.Content); err != nil {
		writeAPIError(writer, http.StatusConflict, err)
		return
	}
	response, err = service.delegationSession(store, bookmark)
	if err != nil {
		writeSessionError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}
