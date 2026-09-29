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
	"sync"
	"time"

	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/workspace"
)

const (
	sessionRegistryVersion  = 1
	sessionRegistryFileName = "studio-sessions.json"
	maximumRegistrySize     = 1 << 20
)

type registeredSession struct {
	ID            string    `json:"id"`
	WorkspaceRoot string    `json:"workspace_root"`
	SessionID     string    `json:"session_id"`
	RegisteredAt  time.Time `json:"registered_at"`
}

type sessionRegistryDocument struct {
	Version int                 `json:"version"`
	Items   []registeredSession `json:"items"`
}

// sessionRegistry is a user-level catalog. Workspace session files remain the
// authority for transcripts, execution state, and delegation children.
type sessionRegistry struct {
	path string
	mu   sync.Mutex
}

func newSessionRegistry(configDirectory string) *sessionRegistry {
	return &sessionRegistry{path: filepath.Join(configDirectory, sessionRegistryFileName)}
}

func (registry *sessionRegistry) list() ([]registeredSession, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	value, err := registry.loadLocked()
	if err != nil {
		return nil, err
	}
	return append([]registeredSession(nil), value.Items...), nil
}

func (registry *sessionRegistry) register(root, sessionID string) (registeredSession, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	value, err := registry.loadLocked()
	if err != nil {
		return registeredSession{}, err
	}
	for _, item := range value.Items {
		if item.WorkspaceRoot == root && item.SessionID == sessionID {
			return item, nil
		}
	}
	id, err := workspace.NewSessionID()
	if err != nil {
		return registeredSession{}, err
	}
	item := registeredSession{ID: id, WorkspaceRoot: root, SessionID: sessionID, RegisteredAt: time.Now().UTC()}
	value.Items = append(value.Items, item)
	if err := registry.saveLocked(value); err != nil {
		return registeredSession{}, err
	}
	return item, nil
}

func (registry *sessionRegistry) unregister(id string) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	value, err := registry.loadLocked()
	if err != nil {
		return err
	}
	for index, item := range value.Items {
		if item.ID != id {
			continue
		}
		value.Items = append(value.Items[:index], value.Items[index+1:]...)
		return registry.saveLocked(value)
	}
	return os.ErrNotExist
}

func (registry *sessionRegistry) loadLocked() (sessionRegistryDocument, error) {
	file, err := os.Open(registry.path)
	if errors.Is(err, os.ErrNotExist) {
		return sessionRegistryDocument{Version: sessionRegistryVersion, Items: []registeredSession{}}, nil
	}
	if err != nil {
		return sessionRegistryDocument{}, fmt.Errorf("open Studio session registry: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return sessionRegistryDocument{}, fmt.Errorf("inspect Studio session registry: %w", err)
	}
	if info.Size() > maximumRegistrySize {
		return sessionRegistryDocument{}, errors.New("Studio session registry is too large")
	}
	var value sessionRegistryDocument
	decoder := json.NewDecoder(io.LimitReader(file, maximumRegistrySize))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return sessionRegistryDocument{}, fmt.Errorf("decode Studio session registry: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return sessionRegistryDocument{}, errors.New("decode Studio session registry: multiple JSON values")
	}
	if err := validateSessionRegistry(value); err != nil {
		return sessionRegistryDocument{}, err
	}
	return value, nil
}

func validateSessionRegistry(value sessionRegistryDocument) error {
	if value.Version != sessionRegistryVersion {
		return fmt.Errorf("unsupported Studio session registry version %d", value.Version)
	}
	ids := make(map[string]bool, len(value.Items))
	sessions := make(map[string]bool, len(value.Items))
	for _, item := range value.Items {
		if _, err := (workspace.Store{Root: item.WorkspaceRoot}).ForSession(item.ID); err != nil {
			return errors.New("invalid Studio registration ID")
		}
		if _, err := (workspace.Store{Root: item.WorkspaceRoot}).ForSession(item.SessionID); err != nil {
			return errors.New("invalid registered session ID")
		}
		if !filepath.IsAbs(item.WorkspaceRoot) || filepath.Clean(item.WorkspaceRoot) != item.WorkspaceRoot || item.RegisteredAt.IsZero() {
			return errors.New("invalid registered session")
		}
		key := item.WorkspaceRoot + "\x00" + item.SessionID
		if ids[item.ID] || sessions[key] {
			return errors.New("duplicate registered session")
		}
		ids[item.ID], sessions[key] = true, true
	}
	return nil
}

func (registry *sessionRegistry) saveLocked(value sessionRegistryDocument) error {
	value.Version = sessionRegistryVersion
	if err := validateSessionRegistry(value); err != nil {
		return err
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if len(body) > maximumRegistrySize {
		return errors.New("Studio session registry is too large")
	}
	directory := filepath.Dir(registry.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create Studio config directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("secure Studio config directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".studio-sessions-*.json")
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
	if err := fsreplace.Replace(temporaryPath, registry.path); err != nil {
		return err
	}
	keep = true
	return nil
}

type registeredSessionTree struct {
	RegistrationID string           `json:"registration_id"`
	WorkspaceRoot  string           `json:"workspace_root"`
	ProjectID      string           `json:"project_id"`
	ProjectName    string           `json:"project_name"`
	Session        sessionSummary   `json:"session"`
	RegisteredAt   time.Time        `json:"registered_at"`
	Delegations    []delegationNode `json:"delegations,omitempty"`
	Issue          string           `json:"issue,omitempty"`
}

type registeredSessionsResponse struct {
	Projects []studioProject         `json:"projects"`
	Sessions []registeredSessionTree `json:"sessions"`
}

type registerSessionRequest struct {
	WorkspaceRoot string `json:"workspace_root"`
	SessionID     string `json:"session_id,omitempty"`
	Create        bool   `json:"create,omitempty"`
}

func (service *sessionsService) serveRegisteredCollection(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		items, err := service.registry.list()
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		projects, err := service.projects.list()
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		trees := make([]registeredSessionTree, 0, len(items))
		for _, item := range items {
			trees = append(trees, service.registeredTree(item, projectForWorkspace(projects, item.WorkspaceRoot)))
		}
		sort.SliceStable(trees, func(left, right int) bool {
			if trees[left].Issue != "" && trees[right].Issue == "" {
				return false
			}
			if trees[left].Issue == "" && trees[right].Issue != "" {
				return true
			}
			return trees[left].Session.UpdatedAt.After(trees[right].Session.UpdatedAt)
		})
		writeJSON(writer, http.StatusOK, registeredSessionsResponse{Projects: projects, Sessions: trees})
	case http.MethodPost:
		var input registerSessionRequest
		if err := decodeSessionRequest(writer, request, &input); err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		var store workspace.Store
		if input.Create {
			if input.SessionID != "" {
				writeAPIError(writer, http.StatusBadRequest, errors.New("session_id must be empty when creating a session"))
				return
			}
			var lock io.Closer
			store, lock, err = workspace.CreateSession(root, "q studio")
			if lock != nil {
				err = errors.Join(err, lock.Close())
			}
		} else {
			store, err = (workspace.Store{Root: root}).ForSession(input.SessionID)
			if err == nil {
				_, err = store.Load()
			}
		}
		if err != nil {
			writeSessionError(writer, err)
			return
		}
		item, err := service.registry.register(root, store.SessionID)
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		var project *studioProject
		projects, projectErr := service.projects.list()
		if projectErr != nil {
			writeAPIError(writer, http.StatusInternalServerError, projectErr)
			return
		}
		project = projectForWorkspace(projects, root)
		writeJSON(writer, http.StatusCreated, service.registeredTree(item, project))
	default:
		writer.Header().Set("Allow", "GET, POST")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (service *sessionsService) serveRegisteredItem(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodDelete {
		writer.Header().Set("Allow", "DELETE")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if err := service.registry.unregister(request.PathValue("registration")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeAPIError(writer, http.StatusNotFound, errors.New("registered session does not exist"))
		} else {
			writeAPIError(writer, http.StatusInternalServerError, err)
		}
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (service *sessionsService) registeredTree(item registeredSession, project *studioProject) registeredSessionTree {
	tree := registeredSessionTree{
		RegistrationID: item.ID, WorkspaceRoot: item.WorkspaceRoot,
		Session: sessionSummary{SessionID: item.SessionID}, RegisteredAt: item.RegisteredAt,
	}
	if project != nil {
		tree.ProjectID = project.ID
		tree.ProjectName = project.Name
	}
	store, err := (workspace.Store{Root: item.WorkspaceRoot}).ForSession(item.SessionID)
	if err != nil {
		tree.Issue = err.Error()
		return tree
	}
	value, err := store.Load()
	if err != nil {
		tree.Issue = err.Error()
		return tree
	}
	tree.Session = detailFromSession(item.WorkspaceRoot, store, value).Session
	remaining := maximumDelegationTreeNodes
	tree.Delegations, err = loadDelegationNodes(store, 0, &remaining)
	if err != nil {
		tree.Issue = err.Error()
	}
	return tree
}

func projectForWorkspace(projects []studioProject, root string) *studioProject {
	key := comparableStudioPath(root)
	for index := range projects {
		for _, candidate := range projects[index].WorkspaceRoots {
			if comparableStudioPath(candidate) == key {
				project := cloneStudioProject(projects[index])
				return &project
			}
		}
	}
	return nil
}
