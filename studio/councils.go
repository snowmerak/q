package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/council"
)

type councilModelProvider interface {
	NewCouncilClient(context.Context) (app.ChatClient, config.Config, error)
}

type councilService struct {
	store    *council.Store
	projects *studioProjectStore
	provider councilModelProvider
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	active   map[string]activeCouncilRun
	wg       sync.WaitGroup
}

type activeCouncilRun struct {
	id     string
	cancel context.CancelFunc
}

type councilInput struct {
	Name          string         `json:"name"`
	Scope         council.Scope  `json:"scope"`
	WorkspaceRoot string         `json:"workspace_root,omitempty"`
	ProjectID     string         `json:"project_id,omitempty"`
	Members       []council.Seat `json:"members"`
	Chair         council.Seat   `json:"chair"`
	Rounds        int            `json:"rounds"`
}

type councilTurnInput struct {
	Prompt string `json:"prompt"`
}
type councilDetail struct {
	Council council.Council `json:"council"`
	Turns   []council.Turn  `json:"turns"`
}

func newCouncilService(parent context.Context, configDir string, projects *studioProjectStore, provider councilModelProvider) *councilService {
	ctx, cancel := context.WithCancel(parent)
	return &councilService{
		store: council.NewStore(configDir), projects: projects, provider: provider,
		ctx: ctx, cancel: cancel, active: make(map[string]activeCouncilRun),
	}
}

func (service *councilService) Close() error {
	if service == nil {
		return nil
	}
	service.cancel()
	service.wg.Wait()
	return service.store.Close()
}

func (service *councilService) serveCollection(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		values, err := service.store.List()
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		writeJSON(writer, http.StatusOK, struct {
			Councils []council.Council `json:"councils"`
		}{Councils: values})
	case http.MethodPost:
		var input councilInput
		if err := decodeSettingsRequest(writer, request, &input); err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		value, err := service.prepare(input)
		if err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		value, err = service.store.Create(value)
		if err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		writeJSON(writer, http.StatusCreated, value)
	default:
		writer.Header().Set("Allow", "GET, POST")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (service *councilService) prepare(input councilInput) (council.Council, error) {
	value := council.Council{
		Name: strings.TrimSpace(input.Name), Scope: input.Scope, WorkspaceRoot: input.WorkspaceRoot,
		ProjectID: input.ProjectID, Members: append([]council.Seat(nil), input.Members...), Chair: input.Chair, Rounds: input.Rounds,
	}
	switch value.Scope {
	case council.Independent:
		if value.WorkspaceRoot != "" || value.ProjectID != "" {
			return council.Council{}, errors.New("independent council cannot have a workspace or project")
		}
	case council.Workspace:
		if value.ProjectID != "" {
			return council.Council{}, errors.New("workspace council cannot have a project ID")
		}
		root, err := canonicalWorkspaceDirectory(value.WorkspaceRoot)
		if err != nil {
			return council.Council{}, err
		}
		value.WorkspaceRoot = root
	case council.Project:
		if value.WorkspaceRoot != "" {
			return council.Council{}, errors.New("project council cannot have a workspace root")
		}
		if _, err := service.projectRoots(value.ProjectID); err != nil {
			return council.Council{}, err
		}
	default:
		return council.Council{}, errors.New("unknown council scope")
	}
	if err := council.Validate(value); err != nil {
		return council.Council{}, err
	}
	return value, nil
}

func (service *councilService) projectRoots(id string) ([]string, error) {
	if !council.ValidID(id) {
		return nil, errors.New("invalid Studio project ID")
	}
	projects, err := service.projects.list()
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		if project.ID == id {
			if len(project.WorkspaceRoots) == 0 {
				return nil, errors.New("Studio project has no workspaces")
			}
			roots := make([]string, 0, len(project.WorkspaceRoots))
			for _, item := range project.WorkspaceRoots {
				root, err := canonicalWorkspaceDirectory(item)
				if err != nil {
					return nil, err
				}
				roots = append(roots, root)
			}
			return roots, nil
		}
	}
	return nil, os.ErrNotExist
}

func (service *councilService) serveItem(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("council")
	value, err := service.store.Load(id)
	if err != nil {
		writeCouncilError(writer, err)
		return
	}
	switch request.Method {
	case http.MethodGet:
		turns, err := service.store.ListTurns(id)
		if err != nil {
			writeCouncilError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, councilDetail{Council: value, Turns: turns})
	case http.MethodPut:
		var input councilInput
		if err := decodeSettingsRequest(writer, request, &input); err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		update, err := service.prepare(input)
		if err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		update.ID = id
		update, err = service.store.Update(update)
		if err != nil {
			writeCouncilError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, update)
	case http.MethodDelete:
		service.mu.Lock()
		_, running := service.active[id]
		service.mu.Unlock()
		if running {
			writeAPIError(writer, http.StatusConflict, errors.New("council has a running turn"))
			return
		}
		if err := service.store.Delete(id); err != nil {
			writeCouncilError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		writer.Header().Set("Allow", "GET, PUT, DELETE")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (service *councilService) serveExport(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", "GET")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	id := request.PathValue("council")
	value, err := service.store.Load(id)
	if err != nil {
		writeCouncilError(writer, err)
		return
	}
	turns, err := service.store.ListTurns(id)
	if err != nil {
		writeCouncilError(writer, err)
		return
	}
	format := request.URL.Query().Get("format")
	var body []byte
	var extension, contentType string
	switch format {
	case "", "md":
		body = []byte(council.ExportMarkdown(value, turns))
		extension, contentType = "md", "text/markdown; charset=utf-8"
	case "json":
		body, err = json.MarshalIndent(councilDetail{Council: value, Turns: turns}, "", "  ")
		if err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		body = append(body, '\n')
		extension, contentType = "json", "application/json; charset=utf-8"
	default:
		writeAPIError(writer, http.StatusBadRequest, errors.New("export format must be md or json"))
		return
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"council-%s.%s\"", id, extension))
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(body)
}

func (service *councilService) serveTurns(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "POST")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	value, err := service.store.Load(request.PathValue("council"))
	if err != nil {
		writeCouncilError(writer, err)
		return
	}
	var input councilTurnInput
	if err := decodeSettingsRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if len(input.Prompt) > 64<<10 {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("question is too long"))
		return
	}
	turn, err := council.NewTurn(value, input.Prompt)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	service.mu.Lock()
	if _, busy := service.active[value.ID]; busy {
		service.mu.Unlock()
		writeAPIError(writer, http.StatusConflict, errors.New("council already has a running turn"))
		return
	}
	if service.ctx.Err() != nil {
		service.mu.Unlock()
		writeAPIError(writer, http.StatusServiceUnavailable, errors.New("Studio is shutting down"))
		return
	}
	ctx, cancel := context.WithTimeout(service.ctx, 10*time.Minute)
	service.active[value.ID] = activeCouncilRun{id: turn.ID, cancel: cancel}
	if err := service.store.SaveTurn(turn); err != nil {
		delete(service.active, value.ID)
		service.mu.Unlock()
		cancel()
		writeCouncilError(writer, err)
		return
	}
	service.wg.Add(1)
	service.mu.Unlock()
	go service.execute(ctx, cancel, value, turn)
	writeJSON(writer, http.StatusAccepted, turn)
}

func (service *councilService) execute(ctx context.Context, cancel context.CancelFunc, value council.Council, turn council.Turn) {
	defer service.wg.Done()
	defer cancel()
	defer func() { service.mu.Lock(); delete(service.active, value.ID); service.mu.Unlock() }()
	fail := func(err error) {
		if errors.Is(err, context.Canceled) {
			turn.Status = "cancelled"
		} else {
			turn.Status = "failed"
		}
		turn.Error = err.Error()
		_ = service.store.SaveTurn(turn)
	}
	if service.provider == nil {
		fail(errors.New("Studio council model runtime is unavailable"))
		return
	}
	model, settings, err := service.provider.NewCouncilClient(ctx)
	if err != nil {
		fail(err)
		return
	}
	defer model.Close()
	var roots []string
	switch value.Scope {
	case council.Workspace:
		roots = []string{value.WorkspaceRoot}
	case council.Project:
		roots, err = service.projectRoots(value.ProjectID)
		if err != nil {
			fail(err)
			return
		}
	}
	previous, err := service.store.ListTurns(value.ID)
	if err != nil {
		fail(err)
		return
	}
	var history []council.Turn
	for _, item := range previous {
		if item.ID != turn.ID {
			history = append(history, item)
		}
	}
	scratch := filepath.Join(service.store.Root, "scratch", turn.ID)
	turn, err = council.Run(ctx, model, value, history, turn, roots, scratch, settings.EffectiveAgents().MaxParallel, func(progress council.Turn) error {
		turn = progress
		return service.store.SaveTurn(progress)
	})
	if err != nil {
		fail(err)
	}
}

func (service *councilService) serveRun(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", "GET")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	id, runID := request.PathValue("council"), request.PathValue("run")
	if !council.ValidID(runID) {
		writeAPIError(writer, http.StatusBadRequest, errors.New("invalid run ID"))
		return
	}
	turns, err := service.store.ListTurns(id)
	if err != nil {
		writeCouncilError(writer, err)
		return
	}
	for _, turn := range turns {
		if turn.ID == runID {
			service.mu.Lock()
			active := service.active[id].id == runID
			service.mu.Unlock()
			if !active && (turn.Status == "queued" || turn.Status == "running") {
				turn.Status, turn.Error = "interrupted", "Studio stopped before this turn completed"
				_ = service.store.SaveTurn(turn)
			}
			writeJSON(writer, http.StatusOK, turn)
			return
		}
	}
	writeAPIError(writer, http.StatusNotFound, os.ErrNotExist)
}

func (service *councilService) serveCancel(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "POST")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	id, runID := request.PathValue("council"), request.PathValue("run")
	turns, err := service.store.ListTurns(id)
	if err != nil {
		writeCouncilError(writer, err)
		return
	}
	found := false
	for _, turn := range turns {
		if turn.ID == runID {
			found = true
			break
		}
	}
	if !found {
		writeAPIError(writer, http.StatusNotFound, os.ErrNotExist)
		return
	}
	service.mu.Lock()
	run := service.active[id]
	service.mu.Unlock()
	if run.id != runID || run.cancel == nil {
		writeAPIError(writer, http.StatusConflict, errors.New("council turn is not running"))
		return
	}
	run.cancel()
	writeJSON(writer, http.StatusAccepted, map[string]string{"status": "cancelling"})
}

func writeCouncilError(writer http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, os.ErrNotExist) {
		status = http.StatusNotFound
	}
	writeAPIError(writer, status, fmt.Errorf("council: %w", err))
}
