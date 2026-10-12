package studio

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/workspace"
)

const maximumEmbeddingSyncJobs = 32

type embeddingArchiveRuntime interface {
	SyncEmbeddingArchive(context.Context, string) (int, error)
}

type embeddingSyncRequest struct {
	Scope         string `json:"scope"`
	WorkspaceRoot string `json:"workspace_root,omitempty"`
}

type embeddingSyncTarget struct {
	Name          string `json:"name"`
	Scope         string `json:"scope"`
	WorkspaceRoot string `json:"workspace_root,omitempty"`
	State         string `json:"state"`
	Embedded      int    `json:"embedded"`
	Error         string `json:"error,omitempty"`
}

type embeddingSyncJob struct {
	ID            string                `json:"id"`
	Scope         string                `json:"scope"`
	WorkspaceRoot string                `json:"workspace_root,omitempty"`
	State         string                `json:"state"`
	Model         string                `json:"model"`
	Dimensions    int                   `json:"dimensions"`
	StartedAt     time.Time             `json:"started_at"`
	CompletedAt   *time.Time            `json:"completed_at,omitempty"`
	Total         int                   `json:"total"`
	Completed     int                   `json:"completed"`
	Failed        int                   `json:"failed"`
	Embedded      int                   `json:"embedded"`
	Error         string                `json:"error,omitempty"`
	Targets       []embeddingSyncTarget `json:"targets"`
}

// Jobs belong to Studio, not an HTTP request. One job runs at a time, so a
// per-archive request cannot overlap a full sync. Source CAS still protects
// against automatic repairs and model changes outside this scheduler.
type embeddingSyncService struct {
	ctx     context.Context
	cancel  context.CancelFunc
	status  embeddingStatusService
	runtime embeddingArchiveRuntime
	mu      sync.Mutex
	wg      sync.WaitGroup
	closing bool
	jobs    map[string]*embeddingSyncJob
	order   []string
	active  string
}

func newEmbeddingSyncService(parent context.Context, status embeddingStatusService, runner sessionRunner) *embeddingSyncService {
	ctx, cancel := context.WithCancel(parent)
	runtime, _ := runner.(embeddingArchiveRuntime)
	return &embeddingSyncService{ctx: ctx, cancel: cancel, status: status, runtime: runtime, jobs: make(map[string]*embeddingSyncJob)}
}

func (service *embeddingSyncService) Close() error {
	if service == nil {
		return nil
	}
	service.mu.Lock()
	service.closing = true
	service.cancel()
	service.mu.Unlock()
	service.wg.Wait()
	return nil
}

func (service *embeddingSyncService) latest() *embeddingSyncJob {
	if service == nil {
		return nil
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.order) == 0 {
		return nil
	}
	return cloneEmbeddingSyncJob(service.jobs[service.order[len(service.order)-1]])
}

func cloneEmbeddingSyncJob(job *embeddingSyncJob) *embeddingSyncJob {
	if job == nil {
		return nil
	}
	copy := *job
	copy.Targets = append([]embeddingSyncTarget{}, job.Targets...)
	return &copy
}

func (service *embeddingSyncService) serveJob(writer http.ResponseWriter, request *http.Request) {
	service.mu.Lock()
	job := cloneEmbeddingSyncJob(service.jobs[request.PathValue("job")])
	service.mu.Unlock()
	if job == nil {
		writeAPIError(writer, http.StatusNotFound, errors.New("sync job not found; jobs are retained for the current Studio process"))
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, job)
}

func (service *embeddingSyncService) serveStart(writer http.ResponseWriter, request *http.Request) {
	var input embeddingSyncRequest
	if err := decodeRequest(writer, request, &input, 64<<10, "decode embedding sync request"); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	input.Scope, input.WorkspaceRoot = strings.TrimSpace(input.Scope), strings.TrimSpace(input.WorkspaceRoot)
	if input.Scope != "all" && input.Scope != "global" && input.Scope != "workspace" || input.Scope == "global" && input.WorkspaceRoot != "" || input.Scope == "workspace" && input.WorkspaceRoot == "" {
		writeAPIError(writer, http.StatusBadRequest, errors.New("scope must be all, global, or workspace; workspace requires workspace_root"))
		return
	}
	if input.WorkspaceRoot != "" {
		if !filepath.IsAbs(input.WorkspaceRoot) {
			writeAPIError(writer, http.StatusBadRequest, errors.New("workspace_root must be an absolute path"))
			return
		}
		root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		input.WorkspaceRoot = root
		if input.Scope == "workspace" {
			info, err := os.Stat(filepath.Join(root, ".q", "data", "records"))
			if err != nil || !info.IsDir() {
				writeAPIError(writer, http.StatusBadRequest, errors.New("workspace has no accessible archive"))
				return
			}
		}
	}
	if service.runtime == nil {
		writeAPIError(writer, http.StatusServiceUnavailable, errors.New("embedding sync runtime is unavailable"))
		return
	}
	value, err := service.status.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if value.Embedding.Model == "" || value.Embedding.Dimensions <= 0 {
		writeAPIError(writer, http.StatusConflict, errors.New("embeddings are disabled"))
		return
	}
	service.mu.Lock()
	if service.closing || service.ctx.Err() != nil {
		service.mu.Unlock()
		writeAPIError(writer, http.StatusServiceUnavailable, errors.New("studio is shutting down"))
		return
	}
	if active := service.jobs[service.active]; active != nil {
		job := cloneEmbeddingSyncJob(active)
		service.mu.Unlock()
		if job.Scope != input.Scope || comparableStudioPath(job.WorkspaceRoot) != comparableStudioPath(input.WorkspaceRoot) {
			writeJSON(writer, http.StatusConflict, map[string]any{"error": "another embedding sync is running", "job": job})
			return
		}
		writeEmbeddingSyncAccepted(writer, job)
		return
	}
	id, err := workspace.NewSessionID()
	if err != nil {
		service.mu.Unlock()
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	job := &embeddingSyncJob{ID: id, Scope: input.Scope, WorkspaceRoot: input.WorkspaceRoot, State: "queued", Model: value.Embedding.Model, Dimensions: value.Embedding.Dimensions, StartedAt: time.Now().UTC(), Targets: []embeddingSyncTarget{}}
	if len(service.order) >= maximumEmbeddingSyncJobs {
		delete(service.jobs, service.order[0])
		service.order = service.order[1:]
	}
	service.jobs[id], service.active = job, id
	service.order = append(service.order, id)
	accepted := cloneEmbeddingSyncJob(job)
	service.wg.Add(1)
	service.mu.Unlock()
	go service.run(id)
	writeEmbeddingSyncAccepted(writer, accepted)
}

func writeEmbeddingSyncAccepted(writer http.ResponseWriter, job *embeddingSyncJob) {
	writer.Header().Set("Location", "/api/v1/embeddings/sync/"+job.ID)
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusAccepted, job)
}

func (service *embeddingSyncService) run(id string) {
	defer service.wg.Done()
	service.mu.Lock()
	job := service.jobs[id]
	job.State = "running"
	scope, root := job.Scope, job.WorkspaceRoot
	service.mu.Unlock()
	var targets []embeddingSyncTarget
	var runErr error
	if scope == "all" {
		ctx, cancel := context.WithTimeout(service.ctx, 30*time.Second)
		snapshot, err := service.status.snapshot(ctx, root)
		cancel()
		if err != nil {
			runErr = err
		} else {
			if !snapshot.Complete {
				runErr = errors.New("archive discovery or inspection was incomplete; see embedding status for details")
			}
			for _, archive := range snapshot.Archives {
				if archive.Exists && archive.State != "current" {
					targets = append(targets, embeddingSyncTarget{Name: archive.Name, Scope: archive.Scope, WorkspaceRoot: archive.WorkspaceRoot, State: "pending"})
				}
			}
		}
	} else {
		name := "Global Library"
		if scope == "workspace" {
			name = filepath.Base(root)
		}
		targets = append(targets, embeddingSyncTarget{Name: name, Scope: scope, WorkspaceRoot: root, State: "pending"})
	}
	service.mu.Lock()
	job.Targets, job.Total = targets, len(targets)
	service.mu.Unlock()
	for index := range targets {
		if err := service.checkConfiguration(job); err != nil {
			runErr = errors.Join(runErr, err)
			break
		}
		service.mu.Lock()
		job.Targets[index].State = "running"
		service.mu.Unlock()
		ctx, cancel := context.WithTimeout(service.ctx, 5*time.Minute)
		embedded, err := service.runtime.SyncEmbeddingArchive(ctx, targets[index].WorkspaceRoot)
		cancel()
		service.mu.Lock()
		job.Targets[index].Embedded, job.Targets[index].State = embedded, "succeeded"
		job.Embedded += embedded
		job.Completed++
		if err != nil {
			job.Targets[index].State, job.Targets[index].Error = "failed", err.Error()
			job.Failed++
			runErr = errors.Join(runErr, fmt.Errorf("%s: %w", targets[index].Name, err))
		}
		service.mu.Unlock()
	}
	runErr = errors.Join(runErr, service.checkConfiguration(job))
	service.mu.Lock()
	defer service.mu.Unlock()
	job.State = "succeeded"
	if runErr != nil {
		job.State, job.Error = "failed", runErr.Error()
	}
	if service.ctx.Err() != nil {
		job.State = "cancelled"
	}
	now := time.Now().UTC()
	job.CompletedAt = &now
	service.active = ""
}

func (service *embeddingSyncService) checkConfiguration(job *embeddingSyncJob) error {
	if err := service.ctx.Err(); err != nil {
		return err
	}
	value, err := service.status.main.Load()
	if err != nil {
		return err
	}
	if value.Embedding.Model != job.Model || value.Embedding.Dimensions != job.Dimensions {
		return errors.New("embedding configuration changed during sync; retry with the current settings")
	}
	return nil
}
