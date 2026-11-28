package studio

import (
	"context"
	"errors"
	"net/http"
	"os"

	"github.com/snowmerak/q/coordinator"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/workspace"
)

func (service *sessionsService) ensureProjectManager(ctx context.Context, project studioProject) (registeredSession, error) {
	store, err := coordinator.EnsureManager(ctx, service.main.Dir, project)
	if err != nil {
		return registeredSession{}, err
	}
	return service.registry.register(store.Root, store.SessionID)
}

func registeredKey(root, sessionID string) string {
	return comparableStudioPath(root) + "\x00" + sessionID
}

func (service *sessionsService) isVerifiedManager(root, sessionID string) bool {
	key := registeredKey(root, sessionID)
	service.verifiedMu.Lock()
	defer service.verifiedMu.Unlock()
	_, ok := service.verifiedManagers[key]
	return ok
}

func (service *sessionsService) markVerifiedManager(root, sessionID string) {
	key := registeredKey(root, sessionID)
	service.verifiedMu.Lock()
	if service.verifiedManagers == nil {
		service.verifiedManagers = make(map[string]struct{})
	}
	service.verifiedManagers[key] = struct{}{}
	service.verifiedMu.Unlock()
}

func (service *sessionsService) ensureProjectManagers(ctx context.Context, projects []studioProject) error {
	for _, project := range projects {
		if _, err := service.ensureProjectManager(ctx, project); err != nil {
			return err
		}
		service.markProjectRoleVerified(coordinator.ManagerStore, project)
		if _, err := service.ensureProjectCoordinator(ctx, project); err != nil {
			return err
		}
		service.markProjectRoleVerified(coordinator.CoordinatorStore, project)
	}
	return nil
}

func (service *sessionsService) markProjectRoleVerified(
	storeFn func(string, workitems.Workspace) (workspace.Store, error),
	project studioProject,
) {
	if store, err := storeFn(service.main.Dir, project); err == nil {
		service.markVerifiedManager(store.Root, store.SessionID)
	}
}

func (service *sessionsService) ensureProjectManagersIfMissing(
	ctx context.Context,
	projects []studioProject,
	existing []registeredSession,
) (bool, error) {
	if len(projects) == 0 {
		return false, nil
	}
	existingSet := make(map[string]struct{}, len(existing))
	for _, item := range existing {
		existingSet[registeredKey(item.WorkspaceRoot, item.SessionID)] = struct{}{}
	}
	changed := false
	for _, project := range projects {
		if ok, err := service.ensureRoleIfMissing(ctx, project, existingSet, coordinator.ManagerStore, service.ensureProjectManager); err != nil {
			return false, err
		} else if ok {
			changed = true
		}
		if ok, err := service.ensureRoleIfMissing(ctx, project, existingSet, coordinator.CoordinatorStore, service.ensureProjectCoordinator); err != nil {
			return false, err
		} else if ok {
			changed = true
		}
	}
	return changed, nil
}

func (service *sessionsService) ensureRoleIfMissing(
	ctx context.Context,
	project studioProject,
	existingSet map[string]struct{},
	storeFn func(string, workitems.Workspace) (workspace.Store, error),
	ensureFn func(context.Context, studioProject) (registeredSession, error),
) (bool, error) {
	store, err := storeFn(service.main.Dir, project)
	if err == nil {
		key := registeredKey(store.Root, store.SessionID)
		if _, registered := existingSet[key]; registered {
			if _, statErr := os.Stat(store.Path()); statErr == nil {
				if service.isVerifiedManager(store.Root, store.SessionID) {
					return false, nil
				}
				if binding, bindErr := store.ManagerBinding(); bindErr == nil && binding != nil &&
					binding.WorkspaceID == project.ID && coordinator.SessionRole(project, store.SessionID) != "" {
					service.markVerifiedManager(store.Root, store.SessionID)
					return false, nil
				}
			}
		}
	}
	if _, err := ensureFn(ctx, project); err != nil {
		return false, err
	}
	service.markProjectRoleVerified(storeFn, project)
	return true, nil
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
		writeJSON(writer, http.StatusOK, service.registeredTree(item, &project, true))
		return
	}
	writeAPIError(writer, http.StatusNotFound, errors.New("studio project does not exist"))
}
