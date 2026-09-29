package studio

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/snowmerak/q/workspace"
)

const (
	maximumDelegationTreeDepth = 16
	maximumDelegationTreeNodes = 256
)

type delegationNode struct {
	Bookmark   workspace.DelegationBookmark `json:"bookmark"`
	State      *workspace.DelegationState   `json:"state,omitempty"`
	Transcript []sessionMessage             `json:"transcript,omitempty"`
	Children   []delegationNode             `json:"children,omitempty"`
	Issue      string                       `json:"issue,omitempty"`
}

type delegationTreeResponse struct {
	WorkspaceRoot string           `json:"workspace_root"`
	SessionID     string           `json:"session_id"`
	Nodes         []delegationNode `json:"nodes"`
}

func (service *sessionsService) serveDelegations(writer http.ResponseWriter, request *http.Request) {
	root, store, ok := sessionStoreFromQuery(writer, request)
	if !ok {
		return
	}
	remaining := maximumDelegationTreeNodes
	nodes, err := loadDelegationNodes(store, 0, &remaining)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(writer, http.StatusOK, delegationTreeResponse{WorkspaceRoot: root, SessionID: store.SessionID, Nodes: nodes})
}

func (service *sessionsService) serveDeleteDelegation(writer http.ResponseWriter, request *http.Request) {
	root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	pathValue := strings.Trim(request.URL.Query().Get("path"), "/")
	if pathValue == "" {
		writeAPIError(writer, http.StatusBadRequest, errors.New("delegation path is required"))
		return
	}
	invocationPath := strings.Split(pathValue, "/")
	if len(invocationPath) > maximumDelegationTreeDepth {
		writeAPIError(writer, http.StatusBadRequest, errors.New("delegation path exceeds maximum depth"))
		return
	}
	if err := workspace.DeleteCompletedDelegation(
		root,
		request.PathValue("session"),
		invocationPath,
		"q studio delegation delete",
	); err != nil {
		switch {
		case errors.Is(err, workspace.ErrDelegationNotFound):
			writeAPIError(writer, http.StatusNotFound, err)
		case errors.Is(err, workspace.ErrDelegationNotCompleted):
			writeAPIError(writer, http.StatusConflict, err)
		default:
			writeSessionError(writer, err)
		}
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func loadDelegationNodes(store workspace.Store, depth int, remaining *int) ([]delegationNode, error) {
	if depth > maximumDelegationTreeDepth {
		return nil, errors.New("delegation tree exceeds maximum depth")
	}
	bookmarks, err := store.LoadDelegations()
	if err != nil {
		return nil, err
	}
	result := make([]delegationNode, 0, len(bookmarks))
	for _, bookmark := range bookmarks {
		if *remaining <= 0 {
			return nil, errors.New("delegation tree exceeds maximum node count")
		}
		*remaining = *remaining - 1
		child, err := store.ChildStore(bookmark.InvocationID)
		if err != nil {
			return nil, err
		}
		node := delegationNode{Bookmark: bookmark}
		state, stateErr := child.LoadDelegationState()
		if stateErr == nil {
			node.State = &state
		} else if !errors.Is(stateErr, os.ErrNotExist) {
			node.Issue = stateErr.Error()
		}
		if session, sessionErr := child.Load(); sessionErr == nil {
			node.Transcript = detailFromSession(store.Root, child, session).Transcript
		} else if !errors.Is(sessionErr, workspace.ErrNotFound) && node.Issue == "" {
			node.Issue = sessionErr.Error()
		}
		children, childErr := loadDelegationNodes(child, depth+1, remaining)
		if childErr != nil {
			return nil, childErr
		}
		node.Children = children
		result = append(result, node)
	}
	return result, nil
}
