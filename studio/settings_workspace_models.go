package studio

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

func (service *settingsService) serveWorkspaceModels(writer http.ResponseWriter, request *http.Request) {
	root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.writeWorkspaceModels(writer, root)
}

func (service *settingsService) serveWorkspaceModelUpdate(writer http.ResponseWriter, request *http.Request) {
	var update workspaceModelUpdate
	if err := decodeSettingsRequest(writer, request, &update); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := canonicalWorkspaceDirectory(update.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	target := request.PathValue("target")
	if !workspace.ModelOverrideAllowed(target) {
		writeAPIError(writer, http.StatusNotFound, fmt.Errorf("model role %q cannot be overridden per workspace", target))
		return
	}
	model := strings.TrimSpace(update.Model)
	if request.Method == http.MethodPut && model == "" {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("workspace model is required"))
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	store := workspace.Store{Root: root}
	value, err := store.LoadModelConfig()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if request.Method == http.MethodDelete {
		delete(value.Overrides, target)
		if len(value.Overrides) == 0 {
			err = store.ClearModelConfig()
		} else {
			err = store.SaveModelConfig(value)
		}
	} else {
		if value.Overrides == nil {
			value.Overrides = make(map[string]workspace.ModelOverride)
		}
		value.Overrides[target] = workspace.ModelOverride{Model: model}
		err = store.SaveModelConfig(value)
	}
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.writeWorkspaceModels(writer, root)
}

func (service *settingsService) writeWorkspaceModels(writer http.ResponseWriter, root string) {
	snapshot, err := service.workspaceModels(root)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, snapshot)
}

func (service *settingsService) workspaceModels(root string) (workspaceModelSettings, error) {
	main, err := service.main.Load()
	if errors.Is(err, config.ErrNotFound) {
		main, err = config.Default(), nil
	}
	if err != nil {
		return workspaceModelSettings{}, err
	}
	store := workspace.Store{Root: root}
	value, err := store.LoadModelConfig()
	if err != nil {
		return workspaceModelSettings{}, err
	}
	targets := append([]string{"default"}, main.NativeRoles()...)
	assignments := make([]workspaceRoleModelAssignment, 0, len(targets))
	for _, role := range targets {
		if !workspace.ModelOverrideAllowed(role) {
			continue
		}
		configured := value.Overrides[role].Model
		effective := configured
		if effective == "" {
			if role == "default" {
				effective = main.Provider.Model
			} else {
				agent, effectiveErr := main.EffectiveAgent(role)
				if effectiveErr != nil {
					return workspaceModelSettings{}, effectiveErr
				}
				effective = agent.Model
				if agent.Group != "" {
					effective = "group/" + agent.Group
				}
			}
		}
		assignments = append(assignments, workspaceRoleModelAssignment{
			Role: role, ConfiguredModel: configured, EffectiveModel: effective, Inherited: configured == "",
		})
	}
	return workspaceModelSettings{WorkspaceRoot: root, ConfigPath: store.ModelPath(), Assignments: assignments}, nil
}
