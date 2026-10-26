package studio

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/snowmerak/q/internal/sessioncatalog"
	"github.com/snowmerak/q/workspace"
)

type registeredSession = sessioncatalog.Entry
type sessionRegistry struct {
	path    string
	catalog *sessioncatalog.Catalog
}

func newSessionRegistry(dir string) *sessionRegistry {
	return &sessionRegistry{path: filepath.Join(dir, "studio-sessions.json"), catalog: sessioncatalog.New(dir)}
}
func (r *sessionRegistry) list() ([]registeredSession, error) { return r.catalog.List() }
func (r *sessionRegistry) register(root, id string) (registeredSession, error) {
	return r.catalog.Register(root, id)
}
func (r *sessionRegistry) unregister(id string) error { return r.catalog.Unregister(id) }

type registeredSessionTree struct {
	Kind           string           `json:"kind,omitempty"`
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
			tree := service.registeredTree(item, projectForWorkspace(projects, item.WorkspaceRoot))
			// Typed workflows remain in their existing views until Sessions UI integration.
			if tree.Issue == "" && tree.Session.Type != workspace.Interaction {
				continue
			}
			trees = append(trees, tree)
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
			var lock *workspace.Lock
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
	items, err := service.registry.list()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	for _, item := range items {
		if item.ID != request.PathValue("registration") {
			continue
		}
		store := workspace.Store{Root: item.WorkspaceRoot, SessionID: item.SessionID}
		if err := store.RequireInteraction(); err != nil && !errors.Is(err, workspace.ErrNotFound) {
			writeSessionError(writer, err)
			return
		}
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
	if service.isChat(item.WorkspaceRoot) {
		tree.Kind = "chat"
	} else if project != nil {
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
