package studio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workspace"
)

type embeddingSyncRunner struct {
	sync func(context.Context, string) (int, error)
}

func (runner *embeddingSyncRunner) Run(context.Context, workspace.Store, string, string, app.SessionEventSink) error {
	return nil
}

func (runner *embeddingSyncRunner) SyncEmbeddingArchive(ctx context.Context, root string) (int, error) {
	return runner.sync(ctx, root)
}

func newEmbeddingSyncTestService(t *testing.T, runner *embeddingSyncRunner) (*embeddingSyncService, string) {
	t.Helper()
	base := t.TempDir()
	main := config.Store{Dir: filepath.Join(base, "settings")}
	value := config.Default()
	value.Provider.Model = "test"
	value.Embedding = config.EmbeddingConfig{Model: "current", Dimensions: 3}
	if err := main.Save(value); err != nil {
		t.Fatal(err)
	}
	status := embeddingStatusService{main: main, sessions: newSessionsService(t.Context(), runner, studioCatalogTestDirectory(t, main.Dir))}
	service := newEmbeddingSyncService(t.Context(), status, runner)
	service.status.sessions.embeddingSync = service
	t.Cleanup(func() { _ = service.Close() })
	return service, base
}

func startEmbeddingSyncTest(t *testing.T, service *embeddingSyncService, input embeddingSyncRequest) (*httptest.ResponseRecorder, embeddingSyncJob) {
	t.Helper()
	response := serveJSON(t, http.HandlerFunc(service.serveStart), http.MethodPost, "/api/v1/embeddings/sync", input)
	var job embeddingSyncJob
	if response.Code == http.StatusAccepted {
		if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
	}
	return response, job
}

func waitEmbeddingSyncTest(t *testing.T, service *embeddingSyncService) *embeddingSyncJob {
	t.Helper()
	deadline := time.After(5 * time.Second)
	timer := time.NewTicker(time.Millisecond)
	defer timer.Stop()
	for {
		job := service.latest()
		if job != nil && job.State != "queued" && job.State != "running" {
			return job
		}
		select {
		case <-deadline:
			t.Fatal("embedding sync did not complete")
		case <-timer.C:
		}
	}
}

func TestEmbeddingSyncDetachedAcceptanceDeduplicationAndShutdown(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls int
	runner := &embeddingSyncRunner{sync: func(ctx context.Context, root string) (int, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 4*time.Minute || time.Until(deadline) > 5*time.Minute {
			t.Errorf("archive deadline = %v", deadline)
		}
		close(started)
		select {
		case <-release:
			return 2, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}}
	service, _ := newEmbeddingSyncTestService(t, runner)
	ctx, cancel := context.WithCancel(t.Context())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/embeddings/sync", nil).WithContext(ctx)
	body := serveJSON(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		service.serveStart(w, r.WithContext(request.Context()))
	}), http.MethodPost, "/api/v1/embeddings/sync", embeddingSyncRequest{Scope: "global"})
	if body.Code != http.StatusAccepted {
		t.Fatalf("accept = %d %s", body.Code, body.Body.String())
	}
	cancel()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("background sync did not start")
	}
	response, duplicate := startEmbeddingSyncTest(t, service, embeddingSyncRequest{Scope: "global"})
	if response.Code != http.StatusAccepted || response.Header().Get("Location") != body.Header().Get("Location") || duplicate.State != "running" {
		t.Fatalf("duplicate = %d %+v", response.Code, duplicate)
	}
	response, _ = startEmbeddingSyncTest(t, service, embeddingSyncRequest{Scope: "all"})
	if response.Code != http.StatusConflict {
		t.Fatalf("overlap = %d", response.Code)
	}
	close(release)
	job := waitEmbeddingSyncTest(t, service)
	if job.State != "succeeded" || job.Embedded != 2 || job.Completed != 1 || calls != 1 {
		t.Fatalf("job = %+v, calls = %d", job, calls)
	}
	job.Targets[0].State = "tampered"
	if service.latest().Targets[0].State != "succeeded" {
		t.Fatal("job snapshots share mutable targets")
	}
	get := httptest.NewRequest(http.MethodGet, body.Header().Get("Location"), nil)
	get.SetPathValue("job", job.ID)
	recorder := httptest.NewRecorder()
	service.serveJob(recorder, get)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("job query = %d", recorder.Code)
	}
	get.SetPathValue("job", "missing")
	recorder = httptest.NewRecorder()
	service.serveJob(recorder, get)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown job = %d", recorder.Code)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	response, _ = startEmbeddingSyncTest(t, service, embeddingSyncRequest{Scope: "global"})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed service = %d", response.Code)
	}
}

func TestEmbeddingSyncAllIncludesUnregisteredArchivesAndContinuesAfterFailure(t *testing.T) {
	var mu sync.Mutex
	var roots []string
	runner := &embeddingSyncRunner{sync: func(ctx context.Context, root string) (int, error) {
		mu.Lock()
		roots = append(roots, root)
		mu.Unlock()
		if filepath.Base(root) == "registered" {
			return 1, errors.New("provider unavailable")
		}
		return 2, nil
	}}
	service, base := newEmbeddingSyncTestService(t, runner)
	registered, unregistered, absent := filepath.Join(base, "registered"), filepath.Join(base, "unregistered"), filepath.Join(base, "absent")
	for _, directory := range []string{filepath.Join(registered, ".q", "data", "records"), filepath.Join(unregistered, ".q", "data", "records"), filepath.Join(service.status.main.Dir, "library", "data", "records"), absent} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Project catalog access starts Library, which can legitimately create a
	// current empty index. Seed actual missing work so global sync is required.
	global, err := sessionstore.OpenWithOptions(service.status.main.Dir, sessionstore.OpenOptions{Directory: "library"})
	if err != nil {
		t.Fatal(err)
	}
	seedGlobalEmbeddingSkill(t, service.status.main.Dir, global, nil)
	if err := global.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.status.sessions.projects.create(studioProjectUpdateRequest{Name: "project", WorkspaceRoots: []string{registered, absent}}); err != nil {
		t.Fatal(err)
	}
	response, _ := startEmbeddingSyncTest(t, service, embeddingSyncRequest{Scope: "all"})
	if response.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", response.Code, response.Body.String())
	}
	job := waitEmbeddingSyncTest(t, service)
	if job.State != "failed" || job.Total != 3 || job.Completed != 3 || job.Failed != 1 || job.Embedded != 5 || job.Targets[2].WorkspaceRoot != unregistered {
		t.Fatalf("all job = %+v", job)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(roots) != 3 || roots[0] != "" || roots[1] != registered || roots[2] != unregistered {
		t.Fatalf("sync roots = %v", roots)
	}
	if _, err := os.Stat(filepath.Join(absent, ".q")); !os.IsNotExist(err) {
		t.Fatalf("sync created absent archive: %v", err)
	}
}

func TestEmbeddingSyncModelChangeCancellationAndValidation(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "model change", true: "shutdown"}[shutdown], func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			runner := &embeddingSyncRunner{sync: func(ctx context.Context, root string) (int, error) {
				close(started)
				select {
				case <-release:
					return 1, nil
				case <-ctx.Done():
					return 0, ctx.Err()
				}
			}}
			service, _ := newEmbeddingSyncTestService(t, runner)
			for _, input := range []embeddingSyncRequest{{Scope: "bad"}, {Scope: "workspace"}, {Scope: "global", WorkspaceRoot: "invalid"}, {Scope: "workspace", WorkspaceRoot: "relative"}} {
				response, _ := startEmbeddingSyncTest(t, service, input)
				if response.Code != http.StatusBadRequest {
					t.Fatalf("invalid input %+v = %d", input, response.Code)
				}
			}
			response, _ := startEmbeddingSyncTest(t, service, embeddingSyncRequest{Scope: "global"})
			if response.Code != http.StatusAccepted {
				t.Fatal(response.Body.String())
			}
			<-started
			if shutdown {
				if err := service.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				value, err := service.status.main.Load()
				if err != nil {
					t.Fatal(err)
				}
				value.Embedding.Model = "changed"
				if err := service.status.main.Save(value); err != nil {
					t.Fatal(err)
				}
				close(release)
			}
			job := waitEmbeddingSyncTest(t, service)
			want := "failed"
			if shutdown {
				want = "cancelled"
			}
			if job.State != want || job.Error == "" {
				t.Fatalf("terminal job = %+v", job)
			}
		})
	}
}
