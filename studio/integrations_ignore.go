package studio

import (
	"errors"
	"net/http"

	"github.com/snowmerak/q/workspace"
)

type ignoreSettingsResponse struct {
	WorkspaceRoot string `json:"workspace_root"`
	Path          string `json:"path"`
	Content       string `json:"content"`
	Revision      string `json:"revision"`
}

type ignoreUpdate struct {
	WorkspaceRoot string `json:"workspace_root"`
	Content       string `json:"content"`
	Revision      string `json:"revision"`
}

func (service *integrationService) serveIgnore(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		service.writeIgnore(writer, root)
		return
	}
	if request.Method != http.MethodPut {
		writer.Header().Set("Allow", "GET, PUT")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	var input ignoreUpdate
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	store := workspace.Store{Root: root}
	current, err := store.LoadIgnore()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if revision([]byte(current)) != input.Revision {
		writeAPIError(writer, http.StatusConflict, errors.New(".qignore changed externally; reload before saving"))
		return
	}
	if err := store.SaveIgnore(input.Content); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeIgnoreUnlocked(writer, root)
}

func (service *integrationService) writeIgnore(writer http.ResponseWriter, root string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.writeIgnoreUnlocked(writer, root)
}

func (service *integrationService) writeIgnoreUnlocked(writer http.ResponseWriter, root string) {
	store := workspace.Store{Root: root}
	content, err := store.LoadIgnore()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, ignoreSettingsResponse{WorkspaceRoot: root, Path: store.IgnorePath(), Content: content, Revision: revision([]byte(content))})
}
