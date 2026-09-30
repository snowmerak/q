package studio

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/snowmerak/q/agentskills"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workspacememory"
)

type skillSettingsResponse struct {
	WorkspaceRoot string              `json:"workspace_root"`
	Skills        []studioSkill       `json:"skills"`
	Issues        []agentskills.Issue `json:"issues"`
}

type studioSkill struct {
	agentskills.Skill
	Active  bool `json:"active"`
	Managed bool `json:"managed"`
}

type skillOperationRequest struct {
	WorkspaceRoot string `json:"workspace_root"`
	Scope         string `json:"scope,omitempty"`
	Repository    string `json:"repository,omitempty"`
	ID            string `json:"id,omitempty"`
}

func (service *integrationService) serveSkills(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		service.writeSkills(writer, root)
	case http.MethodPost:
		var input skillOperationRequest
		if err := decodeIntegrationRequest(writer, request, &input); err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		root, registry, err := service.skillRegistry(input.WorkspaceRoot)
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		if _, err := registry.InstallGit(request.Context(), input.Scope, input.Repository); err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		if err := service.reindexSkills(request.Context(), root, registry); err != nil {
			writeAPIError(writer, http.StatusBadGateway, fmt.Errorf("skill installed, but reindexing failed: %w", err))
			return
		}
		service.writeSkills(writer, root)
	default:
		writer.Header().Set("Allow", "GET, POST")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (service *integrationService) serveSkillItem(writer http.ResponseWriter, request *http.Request) {
	var input skillOperationRequest
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, registry, err := service.skillRegistry(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	id := request.PathValue("skill")
	switch request.Method {
	case http.MethodPost:
		_, err = registry.UpdateGit(request.Context(), id)
	case http.MethodDelete:
		_, err = registry.RemoveGit(id)
	default:
		writer.Header().Set("Allow", "POST, DELETE")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if err := service.reindexSkills(request.Context(), root, registry); err != nil {
		writeAPIError(writer, http.StatusBadGateway, fmt.Errorf("skill changed, but reindexing failed: %w", err))
		return
	}
	service.writeSkills(writer, root)
}

func (service *integrationService) serveSkillReindex(writer http.ResponseWriter, request *http.Request) {
	var input skillOperationRequest
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, registry, err := service.skillRegistry(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if err := service.reindexSkills(request.Context(), root, registry); err != nil {
		writeAPIError(writer, http.StatusBadGateway, err)
		return
	}
	service.writeSkills(writer, root)
}

func (service *integrationService) skillRegistry(rawRoot string) (string, *agentskills.Registry, error) {
	root, err := canonicalWorkspaceDirectory(rawRoot)
	if err != nil {
		return "", nil, err
	}
	registry, err := agentskills.Discover(root)
	return root, registry, err
}

func (service *integrationService) writeSkills(writer http.ResponseWriter, root string) {
	registry, err := agentskills.Discover(root)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	active := make(map[string]bool)
	for _, skill := range registry.Skills() {
		active[skill.ID] = true
	}
	entries := registry.Entries()
	result := make([]studioSkill, 0, len(entries))
	for _, skill := range entries {
		managed := skill.Source == agentskills.SourceUserQ || skill.Source == agentskills.SourceProjectQ
		if managed {
			_, err = os.Stat(filepath.Join(skill.Directory, ".git"))
			managed = err == nil
		}
		result = append(result, studioSkill{Skill: skill, Active: active[skill.ID], Managed: managed})
	}
	issues := append([]agentskills.Issue{}, registry.Issues()...)
	writeJSON(writer, http.StatusOK, skillSettingsResponse{WorkspaceRoot: root, Skills: result, Issues: issues})
}

func (service *integrationService) reindexSkills(ctx context.Context, root string, registry *agentskills.Registry) (returnErr error) {
	service.mu.Lock()
	value, err := service.main.Load()
	service.mu.Unlock()
	if err != nil {
		return err
	}
	vector := sessionstore.VectorConfig{}
	if value.Embedding.Model != "" {
		vector = sessionstore.VectorConfig{Model: value.Embedding.Model, Dimensions: value.Embedding.Dimensions}
	}
	memoryRuntime, err := workspacememory.Ensure(ctx, service.main.Dir)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, memoryRuntime.Close()) }()
	memoryStore, err := memoryRuntime.Client().OpenWorkspace(ctx, root, vector)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, memoryStore.Close()) }()
	if err := registry.SyncRecordsForScopes(ctx, memoryStore, "project"); err != nil {
		return err
	}
	libraryRuntime, err := qlibrary.Ensure(ctx, service.main.Dir)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, libraryRuntime.Close()) }()
	if _, err := libraryRuntime.Client().ReloadSkills(ctx); err != nil {
		return err
	}
	if service.runtime != nil {
		return service.runtime.SyncEmbeddings(ctx, root, value)
	}
	return nil
}
