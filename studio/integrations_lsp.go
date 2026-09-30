package studio

import (
	"errors"
	"fmt"
	"net/http"

	qlsp "github.com/snowmerak/q/lsp"
	"github.com/snowmerak/q/workspace"
)

type lspSettingsResponse struct {
	ConfigPath    string               `json:"config_path"`
	WorkspacePath string               `json:"workspace_path"`
	WorkspaceRoot string               `json:"workspace_root"`
	Global        qlsp.GlobalConfig    `json:"global"`
	Workspace     qlsp.WorkspaceConfig `json:"workspace"`
}

type lspSettingsUpdate struct {
	WorkspaceRoot string               `json:"workspace_root"`
	Global        qlsp.GlobalConfig    `json:"global"`
	Workspace     qlsp.WorkspaceConfig `json:"workspace"`
}

type lspDiscoveryResponse struct {
	Roots   []qlsp.RootConfig       `json:"roots"`
	Servers []qlsp.DiscoveredServer `json:"servers"`
}

func (service *integrationService) serveLSP(writer http.ResponseWriter, request *http.Request) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if request.Method == http.MethodGet {
		root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		service.writeLSP(writer, root)
		return
	}
	if request.Method != http.MethodPut {
		writer.Header().Set("Allow", "GET, PUT")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	var input lspSettingsUpdate
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	global, err := input.Global.Normalized()
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	value.LSP = global
	if err := value.Validate(); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if err := input.Workspace.Validate(global); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if err := service.main.Save(value); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if err := (workspace.Store{Root: root}).SaveLSP(input.Workspace, global); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, fmt.Errorf("global LSP settings saved, but workspace settings failed: %w", err))
		return
	}
	service.writeLSP(writer, root)
}

func (service *integrationService) writeLSP(writer http.ResponseWriter, root string) {
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	store := workspace.Store{Root: root}
	workspaceConfig, err := store.LoadLSP()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, lspSettingsResponse{
		ConfigPath: service.main.Path(), WorkspacePath: store.LSPPath(), WorkspaceRoot: root,
		Global: value.LSP, Workspace: workspaceConfig,
	})
}

func (service *integrationService) serveLSPDiscover(writer http.ResponseWriter, request *http.Request) {
	var input workspaceRequest
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	roots, err := (workspace.Store{Root: root}).DiscoverLSPRootsContext(request.Context())
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	languages := make([]string, 0, len(roots))
	for _, root := range roots {
		languages = append(languages, root.Language)
	}
	writeJSON(writer, http.StatusOK, lspDiscoveryResponse{Roots: roots, Servers: qlsp.DiscoverServers(languages)})
}
