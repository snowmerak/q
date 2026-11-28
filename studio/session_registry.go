package studio

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/snowmerak/q/coordinator"
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
		includeDelegations := true
		if raw := request.URL.Query().Get("include_delegations"); raw != "" {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				writeAPIError(writer, http.StatusBadRequest, errors.New("include_delegations must be a boolean"))
				return
			}
			includeDelegations = parsed
		}
		limit := 0
		if raw := request.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 200 {
				writeAPIError(writer, http.StatusBadRequest, errors.New("limit must be between 1 and 200"))
				return
			}
			limit = parsed
		}

		projects, err := service.projects.list()
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		items, err := service.registry.list()
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		changed, err := service.ensureProjectManagersIfMissing(request.Context(), projects, items)
		if err != nil {
			writeSessionError(writer, err)
			return
		}
		if changed {
			items, err = service.registry.list()
			if err != nil {
				writeAPIError(writer, http.StatusInternalServerError, err)
				return
			}
		}

		type treeSlot struct {
			tree registeredSessionTree
			keep bool
			err  error
		}
		slots := make([]treeSlot, len(items))
		if len(items) > 0 {
			workers := min(len(items), max(4, runtime.GOMAXPROCS(0)))
			jobs := make(chan int, len(items))
			for idx := range items {
				jobs <- idx
			}
			close(jobs)
			var wg sync.WaitGroup
			wg.Add(workers)
			for range workers {
				go func() {
					defer wg.Done()
					for idx := range jobs {
						item := items[idx]
						project, managed, projErr := service.projectForRegistration(projects, item)
						if projErr != nil {
							slots[idx].err = projErr
							continue
						}
						if managed && project == nil {
							continue
						}
						slots[idx] = treeSlot{
							tree: service.registeredTree(item, project, includeDelegations),
							keep: true,
						}
					}
				}()
			}
			wg.Wait()
		}

		trees := make([]registeredSessionTree, 0, len(items))
		for _, slot := range slots {
			if slot.err != nil {
				writeSessionError(writer, slot.err)
				return
			}
			if slot.keep {
				trees = append(trees, slot.tree)
			}
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
		if limit > 0 && len(trees) > limit {
			trees = trees[:limit]
		}
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
		project, _, err = service.projectForRegistration(projects, item)
		if err != nil {
			writeSessionError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, service.registeredTree(item, project, true))
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
		if err := store.RequireDeletable(); err != nil {
			writeSessionError(writer, err)
			return
		}
		if err := store.RequireInteraction(); err != nil && !errors.Is(err, workspace.ErrNotFound) {
			writeSessionError(writer, err)
			return
		}
	}
	if err := service.registry.unregister(request.PathValue("registration")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeAPIError(writer, http.StatusNotFound, errors.New("registered session does not exist"))
		} else {
			writeSessionError(writer, err)
		}
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (service *sessionsService) summaryLoader() *sessionSummaryCache {
	if service.summaryCache == nil {
		return newSessionSummaryCache()
	}
	return service.summaryCache
}

func (service *sessionsService) registeredTree(item registeredSession, project *studioProject, includeDelegations bool) registeredSessionTree {
	tree := registeredSessionTree{
		RegistrationID: item.ID, WorkspaceRoot: item.WorkspaceRoot,
		Session: sessionSummary{SessionID: item.SessionID}, RegisteredAt: item.RegisteredAt,
	}
	if service.isChat(item.WorkspaceRoot) {
		tree.Kind = "chat"
	} else if project != nil {
		tree.ProjectID = project.ID
		tree.ProjectName = project.Name
		tree.Kind = coordinator.SessionRole(*project, item.SessionID)
	}
	store, err := (workspace.Store{Root: item.WorkspaceRoot}).ForSession(item.SessionID)
	if err != nil {
		tree.Issue = err.Error()
		return tree
	}
	summary, err := service.summaryLoader().load(store)
	if err != nil {
		tree.Issue = err.Error()
		return tree
	}
	sessionType := summary.Type.Effective()
	tree.Session = sessionSummary{
		Type:      sessionType,
		SessionID: store.SessionID,
		RunID:     summary.RunID,
		Title:     summary.Title,
		UpdatedAt: summary.UpdatedAt,
	}
	if !includeDelegations || sessionType != workspace.Interaction {
		return tree
	}
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
