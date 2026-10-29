package studio

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/snowmerak/q/app"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/workitems"
)

const maximumProjectWorkspaceRoots = 32

type studioProject = workitems.Workspace
type studioProjectUpdateRequest = workitems.WorkspaceInput

type studioProjectsResponse struct {
	Projects []studioProject `json:"projects"`
}

type studioProjectStore struct{ directory string }

func newStudioProjectStore(configDirectory string) *studioProjectStore {
	return &studioProjectStore{directory: configDirectory}
}

// Studio retains its project API, but Library is the only catalog writer.
// In normal use SessionHost already hosts Library; a standalone embedder can
// temporarily own it for this bounded operation and then release it.
func withProjectCatalog[T any](store *studioProjectStore, action func(context.Context, *qlibrary.Client) (T, error)) (result T, resultErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runtime, err := qlibrary.Ensure(ctx, store.directory)
	if err != nil {
		return result, err
	}
	defer func() { resultErr = errors.Join(resultErr, runtime.Close()) }()
	result, resultErr = action(ctx, runtime.Client())
	if response, ok := errors.AsType[*qlibrary.ResponseError](resultErr); ok && response.StatusCode == http.StatusNotFound {
		resultErr = os.ErrNotExist
	}
	return result, resultErr
}

func (store *studioProjectStore) list() ([]studioProject, error) {
	return withProjectCatalog(store, func(ctx context.Context, client *qlibrary.Client) ([]studioProject, error) {
		result, err := client.ListWorkspaces(ctx, false)
		return result.Workspaces, err
	})
}

func (store *studioProjectStore) create(input studioProjectUpdateRequest) (studioProject, error) {
	return withProjectCatalog(store, func(ctx context.Context, client *qlibrary.Client) (studioProject, error) {
		return client.CreateWorkspace(ctx, input)
	})
}

func (store *studioProjectStore) update(id string, input studioProjectUpdateRequest) (studioProject, error) {
	return withProjectCatalog(store, func(ctx context.Context, client *qlibrary.Client) (studioProject, error) {
		return client.UpdateWorkspace(ctx, id, input)
	})
}

func (store *studioProjectStore) delete(id string) error {
	_, err := withProjectCatalog(store, func(ctx context.Context, client *qlibrary.Client) (struct{}, error) {
		return struct{}{}, client.ArchiveWorkspace(ctx, id)
	})
	return err
}

func (store *studioProjectStore) resolve(primaryRoot string) (app.SessionWorkspaceContext, bool, error) {
	projects, err := store.list()
	if err != nil {
		return app.SessionWorkspaceContext{}, false, err
	}
	primaryKey := comparableStudioPath(primaryRoot)
	for _, project := range projects {
		matched := false
		auxiliary := make([]string, 0, len(project.WorkspaceRoots)-1)
		for _, root := range project.WorkspaceRoots {
			if comparableStudioPath(root) == primaryKey {
				matched = true
				continue
			}
			auxiliary = append(auxiliary, root)
		}
		if matched {
			return app.SessionWorkspaceContext{ProjectID: project.ID, ProjectName: project.Name, AuxiliaryRoots: auxiliary}, true, nil
		}
	}
	return app.SessionWorkspaceContext{}, false, nil
}

func canonicalProjectInput(input studioProjectUpdateRequest) (studioProjectUpdateRequest, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 128 {
		return studioProjectUpdateRequest{}, errors.New("project name must contain 1 to 128 characters")
	}
	if len(input.WorkspaceRoots) == 0 || len(input.WorkspaceRoots) > maximumProjectWorkspaceRoots {
		return studioProjectUpdateRequest{}, fmt.Errorf("project must contain 1 to %d workspace directories", maximumProjectWorkspaceRoots)
	}
	canonical := make([]string, 0, len(input.WorkspaceRoots))
	seen := make(map[string]struct{}, len(input.WorkspaceRoots))
	for _, value := range input.WorkspaceRoots {
		root, err := canonicalWorkspaceDirectory(value)
		if err != nil {
			return studioProjectUpdateRequest{}, fmt.Errorf("workspace %q: %w", value, err)
		}
		key := comparableStudioPath(root)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		canonical = append(canonical, root)
	}
	input.WorkspaceRoots = canonical
	return input, nil
}

func comparableStudioPath(value string) string {
	value = filepath.Clean(value)
	if filepath.Separator == '\\' {
		return strings.ToLower(value)
	}
	return value
}

func cloneStudioProject(value studioProject) studioProject {
	value.WorkspaceRoots = append([]string(nil), value.WorkspaceRoots...)
	return value
}

func (service *sessionsService) serveProjects(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		projects, err := service.projects.list()
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		if err := service.ensureProjectManagers(request.Context(), projects); err != nil {
			writeSessionError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, studioProjectsResponse{Projects: projects})
	case http.MethodPost:
		var input studioProjectUpdateRequest
		if err := decodeSessionRequest(writer, request, &input); err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		input, err := canonicalProjectInput(input)
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		project, err := service.projects.create(input)
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		if err := service.ensureProjectManagers(request.Context(), []studioProject{project}); err != nil {
			writeSessionError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, project)
	default:
		writer.Header().Set("Allow", "GET, POST")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (service *sessionsService) serveProject(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("project")
	switch request.Method {
	case http.MethodPut:
		var input studioProjectUpdateRequest
		if err := decodeSessionRequest(writer, request, &input); err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		input, err := canonicalProjectInput(input)
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		project, err := service.projects.update(id, input)
		if errors.Is(err, os.ErrNotExist) {
			writeAPIError(writer, http.StatusNotFound, errors.New("studio project does not exist"))
			return
		}
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		writeJSON(writer, http.StatusOK, project)
	case http.MethodDelete:
		err := service.projects.delete(id)
		if errors.Is(err, os.ErrNotExist) {
			writeAPIError(writer, http.StatusNotFound, errors.New("studio project does not exist"))
			return
		}
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	default:
		writer.Header().Set("Allow", "PUT, DELETE")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}
