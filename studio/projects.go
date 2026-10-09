package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/workspace"
)

const (
	studioProjectsVersion        = 1
	studioProjectsFileName       = "studio-projects.json"
	maximumStudioProjectsSize    = 1 << 20
	maximumProjectWorkspaceRoots = 32
)

type studioProject struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	WorkspaceRoots []string  `json:"workspace_roots"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type studioProjectsDocument struct {
	Version  int             `json:"version"`
	Projects []studioProject `json:"projects"`
}

type studioProjectsResponse struct {
	Projects []studioProject `json:"projects"`
}

type studioProjectUpdateRequest struct {
	Name           string   `json:"name"`
	WorkspaceRoots []string `json:"workspace_roots"`
}

type studioProjectStore struct {
	path string
	mu   sync.Mutex
}

func newStudioProjectStore(configDirectory string) *studioProjectStore {
	return &studioProjectStore{path: filepath.Join(configDirectory, studioProjectsFileName)}
}

func (store *studioProjectStore) list() ([]studioProject, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, err := store.loadLocked()
	if err != nil {
		return nil, err
	}
	projects := cloneStudioProjects(value.Projects)
	sort.SliceStable(projects, func(left, right int) bool {
		return strings.ToLower(projects[left].Name) < strings.ToLower(projects[right].Name)
	})
	return projects, nil
}

func (store *studioProjectStore) create(input studioProjectUpdateRequest) (studioProject, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, err := store.loadLocked()
	if err != nil {
		return studioProject{}, err
	}
	id, err := workspace.NewSessionID()
	if err != nil {
		return studioProject{}, err
	}
	now := time.Now().UTC()
	project := studioProject{
		ID: id, Name: strings.TrimSpace(input.Name), WorkspaceRoots: append([]string(nil), input.WorkspaceRoots...),
		CreatedAt: now, UpdatedAt: now,
	}
	value.Projects = append(value.Projects, project)
	if err := store.saveLocked(value); err != nil {
		return studioProject{}, err
	}
	return project, nil
}

func (store *studioProjectStore) update(id string, input studioProjectUpdateRequest) (studioProject, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, err := store.loadLocked()
	if err != nil {
		return studioProject{}, err
	}
	for index := range value.Projects {
		if value.Projects[index].ID != id {
			continue
		}
		value.Projects[index].Name = strings.TrimSpace(input.Name)
		value.Projects[index].WorkspaceRoots = append([]string(nil), input.WorkspaceRoots...)
		value.Projects[index].UpdatedAt = time.Now().UTC()
		if err := store.saveLocked(value); err != nil {
			return studioProject{}, err
		}
		return cloneStudioProject(value.Projects[index]), nil
	}
	return studioProject{}, os.ErrNotExist
}

func (store *studioProjectStore) delete(id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, err := store.loadLocked()
	if err != nil {
		return err
	}
	for index, project := range value.Projects {
		if project.ID != id {
			continue
		}
		value.Projects = append(value.Projects[:index], value.Projects[index+1:]...)
		return store.saveLocked(value)
	}
	return os.ErrNotExist
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
			return app.SessionWorkspaceContext{
				ProjectID: project.ID, ProjectName: project.Name, AuxiliaryRoots: auxiliary,
			}, true, nil
		}
	}
	return app.SessionWorkspaceContext{}, false, nil
}

func (store *studioProjectStore) loadLocked() (studioProjectsDocument, error) {
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return studioProjectsDocument{Version: studioProjectsVersion, Projects: []studioProject{}}, nil
	}
	if err != nil {
		return studioProjectsDocument{}, fmt.Errorf("open Studio projects: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return studioProjectsDocument{}, fmt.Errorf("inspect Studio projects: %w", err)
	}
	if info.Size() > maximumStudioProjectsSize {
		return studioProjectsDocument{}, errors.New("studio projects file is too large")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maximumStudioProjectsSize+1))
	decoder.DisallowUnknownFields()
	var value studioProjectsDocument
	if err := decoder.Decode(&value); err != nil {
		return studioProjectsDocument{}, fmt.Errorf("decode Studio projects: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return studioProjectsDocument{}, errors.New("decode Studio projects: multiple JSON values")
	}
	if err := validateStudioProjects(value); err != nil {
		return studioProjectsDocument{}, err
	}
	value.Projects = cloneStudioProjects(value.Projects)
	return value, nil
}

func (store *studioProjectStore) saveLocked(value studioProjectsDocument) error {
	value.Version = studioProjectsVersion
	if err := validateStudioProjects(value); err != nil {
		return err
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if len(body) > maximumStudioProjectsSize {
		return errors.New("studio projects file is too large")
	}
	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create Studio config directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("secure Studio config directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".studio-projects-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := fsreplace.Replace(temporaryPath, store.path); err != nil {
		return err
	}
	keep = true
	return nil
}

func validateStudioProjects(value studioProjectsDocument) error {
	if value.Version != studioProjectsVersion {
		return fmt.Errorf("unsupported Studio projects version %d", value.Version)
	}
	ids := make(map[string]struct{}, len(value.Projects))
	names := make(map[string]struct{}, len(value.Projects))
	roots := make(map[string]string)
	for _, project := range value.Projects {
		if _, err := (workspace.Store{Root: os.TempDir()}).ForSession(project.ID); err != nil {
			return errors.New("invalid Studio project ID")
		}
		name := strings.TrimSpace(project.Name)
		if name == "" || name != project.Name || len(name) > 128 {
			return errors.New("studio project names must contain 1 to 128 characters without surrounding whitespace")
		}
		if project.CreatedAt.IsZero() || project.UpdatedAt.IsZero() {
			return errors.New("studio project timestamps are required")
		}
		if len(project.WorkspaceRoots) == 0 || len(project.WorkspaceRoots) > maximumProjectWorkspaceRoots {
			return fmt.Errorf("studio projects must contain 1 to %d workspace roots", maximumProjectWorkspaceRoots)
		}
		if _, exists := ids[project.ID]; exists {
			return errors.New("duplicate Studio project ID")
		}
		nameKey := strings.ToLower(name)
		if _, exists := names[nameKey]; exists {
			return fmt.Errorf("duplicate Studio project name %q", name)
		}
		ids[project.ID] = struct{}{}
		names[nameKey] = struct{}{}
		for _, root := range project.WorkspaceRoots {
			if !filepath.IsAbs(root) || filepath.Clean(root) != root {
				return fmt.Errorf("invalid workspace root %q in Studio project %q", root, name)
			}
			key := comparableStudioPath(root)
			if owner, exists := roots[key]; exists {
				return fmt.Errorf("workspace root %q belongs to both %q and %q", root, owner, name)
			}
			roots[key] = name
		}
	}
	return nil
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

func cloneStudioProjects(values []studioProject) []studioProject {
	result := make([]studioProject, len(values))
	for index, value := range values {
		result[index] = cloneStudioProject(value)
	}
	return result
}

func (service *sessionsService) serveProjects(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		projects, err := service.projects.list()
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
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
