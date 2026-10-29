package studio

import (
	"context"
	"errors"
	"net/http"
	"os"

	"github.com/snowmerak/q/coordinator"
	"github.com/snowmerak/q/workspace"
)

func (service *sessionsService) ensureProjectManager(ctx context.Context, project studioProject) (registeredSession, error) {
	store, err := coordinator.EnsureManager(ctx, service.main.Dir, project)
	if err != nil {
		return registeredSession{}, err
	}
	return service.registry.register(store.Root, store.SessionID)
}

func (service *sessionsService) ensureProjectManagers(ctx context.Context, projects []studioProject) error {
	for _, project := range projects {
		if _, err := service.ensureProjectManager(ctx, project); err != nil {
			return err
		}
		if _, err := service.ensureProjectCoordinator(ctx, project); err != nil {
			return err
		}
	}
	return nil
}

func (service *sessionsService) ensureProjectCoordinator(ctx context.Context, project studioProject) (registeredSession, error) {
	store, err := coordinator.EnsureCoordinator(ctx, service.main.Dir, project)
	if err != nil {
		return registeredSession{}, err
	}
	return service.registry.register(store.Root, store.SessionID)
}

func (service *sessionsService) projectForRegistration(projects []studioProject, item registeredSession) (*studioProject, bool, error) {
	binding, err := (workspace.Store{Root: item.WorkspaceRoot, SessionID: item.SessionID}).ManagerBinding()
	if err != nil {
		return nil, false, err
	}
	if binding == nil {
		return projectForWorkspace(projects, item.WorkspaceRoot), false, nil
	}
	for _, project := range projects {
		if project.ID != binding.WorkspaceID || coordinator.SessionRole(project, item.SessionID) == "" {
			continue
		}
		expected, err := coordinator.ManagerStore(service.main.Dir, project)
		if coordinator.SessionRole(project, item.SessionID) == coordinator.RoleCoordinator {
			expected, err = coordinator.CoordinatorStore(service.main.Dir, project)
		}
		if err != nil {
			return nil, true, err
		}
		left, leftErr := os.Stat(expected.Root)
		right, rightErr := os.Stat(item.WorkspaceRoot)
		if leftErr != nil || rightErr != nil || !os.SameFile(left, right) {
			return nil, true, errors.New("manager storage root does not match its project")
		}
		return &project, true, nil
	}
	// Archived projects retain their manager files and registration, but are
	// hidden from the active project tree and cannot execute new turns.
	return nil, true, nil
}

func (service *sessionsService) serveProjectManager(writer http.ResponseWriter, request *http.Request) {
	projects, err := service.projects.list()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	for _, project := range projects {
		if project.ID != request.PathValue("project") {
			continue
		}
		var item registeredSession
		if request.URL.Path == "/api/v1/projects/"+project.ID+"/coordinator" {
			item, err = service.ensureProjectCoordinator(request.Context(), project)
		} else {
			item, err = service.ensureProjectManager(request.Context(), project)
		}
		if err != nil {
			writeSessionError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, service.registeredTree(item, &project))
		return
	}
	writeAPIError(writer, http.StatusNotFound, errors.New("studio project does not exist"))
}
