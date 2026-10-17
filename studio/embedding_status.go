package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/snowmerak/q/archiveembed"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/sessionstore"
)

const maximumEmbeddingDiscoveryEntries = 1000

type embeddingArchiveStatus struct {
	Name          string `json:"name"`
	Scope         string `json:"scope"`
	WorkspaceRoot string `json:"workspace_root,omitempty"`
	Registered    bool   `json:"registered"`
	State         string `json:"state"`
	sessionstore.EmbeddingStatus
}

type embeddingStatusResponse struct {
	Model          string                   `json:"model"`
	Dimensions     int                      `json:"dimensions"`
	Enabled        bool                     `json:"enabled"`
	ObservedAt     time.Time                `json:"observed_at"`
	CompletedAt    time.Time                `json:"completed_at"`
	Complete       bool                     `json:"complete"`
	DiscoveryRoots []string                 `json:"discovery_roots"`
	Warnings       []string                 `json:"warnings"`
	Archives       []embeddingArchiveStatus `json:"archives"`
	SyncJob        *embeddingSyncJob        `json:"sync_job,omitempty"`
}

type embeddingStatusService struct {
	main     config.Store
	sessions *sessionsService
}

// Stream metadata and individual archives without repeating earlier rows. JSON
// callers keep the complete snapshot contract used by embedding sync jobs.
type embeddingStatusEvent struct {
	Type     string                   `json:"type"`
	Snapshot *embeddingStatusResponse `json:"snapshot,omitempty"`
	Archive  *embeddingArchiveStatus  `json:"archive,omitempty"`
	Error    string                   `json:"error,omitempty"`
}

func (service embeddingStatusService) serveStatus(writer http.ResponseWriter, request *http.Request) {
	extraRoot := strings.TrimSpace(request.URL.Query().Get("workspace_root"))
	if extraRoot != "" {
		if !filepath.IsAbs(extraRoot) {
			writeAPIError(writer, http.StatusBadRequest, errors.New("workspace_root must be an absolute path"))
			return
		}
		var err error
		extraRoot, err = canonicalWorkspaceDirectory(extraRoot)
		if err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
	}
	ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
	defer cancel()
	if strings.Contains(request.Header.Get("Accept"), "application/x-ndjson") {
		service.serveStatusStream(ctx, writer, request, extraRoot)
		return
	}
	result, err := service.snapshot(ctx, extraRoot)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		} else if errors.Is(err, context.Canceled) {
			status = 499
		}
		writeAPIError(writer, status, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, result)
}

func (service embeddingStatusService) serveStatusStream(ctx context.Context, writer http.ResponseWriter, request *http.Request, extraRoot string) {
	started := false
	encoder := json.NewEncoder(writer)
	emit := func(event embeddingStatusEvent) error {
		if !started {
			writer.Header().Set("Content-Type", "application/x-ndjson")
			writer.Header().Set("Cache-Control", "no-store")
			writer.Header().Set("X-Accel-Buffering", "no")
			started = true
		}
		if err := encoder.Encode(event); err != nil {
			return err
		}
		return http.NewResponseController(writer).Flush()
	}
	if _, err := service.scan(ctx, extraRoot, emit); err != nil {
		if !started {
			writeAPIError(writer, http.StatusInternalServerError, err)
		} else if request.Context().Err() == nil {
			_ = emit(embeddingStatusEvent{Type: "error", Error: err.Error()})
		}
	}
}

func (service embeddingStatusService) snapshot(ctx context.Context, extraRoot string) (embeddingStatusResponse, error) {
	return service.scan(ctx, extraRoot, nil)
}

func (service embeddingStatusService) scan(ctx context.Context, extraRoot string, emit func(embeddingStatusEvent) error) (embeddingStatusResponse, error) {
	result := embeddingStatusResponse{ObservedAt: time.Now().UTC(), Complete: true, Archives: []embeddingArchiveStatus{}, DiscoveryRoots: []string{}, Warnings: []string{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	value, err := service.main.Load()
	if errors.Is(err, config.ErrNotFound) {
		value, err = config.Default(), nil
	}
	if err != nil {
		return result, err
	}
	vector := sessionstore.VectorConfig{Model: value.Embedding.Model, Dimensions: value.Embedding.Dimensions}
	result.Model, result.Dimensions, result.Enabled = vector.Model, vector.Dimensions, vector.Enabled()
	result.SyncJob = service.sessions.embeddingSync.latest()
	publishSnapshot := func(kind string) error {
		if emit == nil {
			return nil
		}
		metadata := result
		metadata.Archives = []embeddingArchiveStatus{}
		return emit(embeddingStatusEvent{Type: kind, Snapshot: &metadata})
	}
	appendArchive := func(archive embeddingArchiveStatus, err error) error {
		appendEmbeddingArchive(&result, archive, vector, err)
		if emit == nil {
			return nil
		}
		archive = result.Archives[len(result.Archives)-1]
		return emit(embeddingStatusEvent{Type: "archive", Archive: &archive})
	}
	if err := publishSnapshot("started"); err != nil {
		return result, err
	}
	roots, err := service.sessions.embeddingWorkspaceRoots()
	if err != nil {
		return result, err
	}
	registered := make(map[string]bool)
	workspaces := make(map[string]string)
	parents := make(map[string]string)
	add := func(root string, known bool) {
		root = filepath.Clean(root)
		key := comparableStudioPath(root)
		workspaces[key] = root
		registered[key] = registered[key] || known
		parent := filepath.Dir(root)
		if parent != root {
			parents[comparableStudioPath(parent)] = parent
		}
	}
	for _, root := range roots {
		add(root, true)
	}
	if extraRoot != "" {
		add(extraRoot, false)
	}
	for _, parent := range parents {
		result.DiscoveryRoots = append(result.DiscoveryRoots, parent)
	}
	sort.Strings(result.DiscoveryRoots)
	// Discover sibling archives without walking repositories, following symlinks,
	// or assuming a particular projects directory. The response names this scope.
	for _, parent := range result.DiscoveryRoots {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		entries, err := os.ReadDir(parent)
		if err != nil {
			result.Complete = false
			result.Warnings = append(result.Warnings, fmt.Sprintf("Discover %s: %v", parent, err))
			continue
		}
		if len(entries) > maximumEmbeddingDiscoveryEntries {
			entries = entries[:maximumEmbeddingDiscoveryEntries]
			result.Complete = false
			result.Warnings = append(result.Warnings, fmt.Sprintf("Discovery of %s limited to %d entries", parent, maximumEmbeddingDiscoveryEntries))
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			root := filepath.Join(parent, entry.Name())
			info, err := os.Stat(filepath.Join(root, ".q", "data", "records"))
			if err == nil && info.IsDir() {
				workspaces[comparableStudioPath(root)] = root
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				result.Complete = false
				result.Warnings = append(result.Warnings, fmt.Sprintf("Inspect %s: %v", root, err))
			}
		}
	}
	global := embeddingArchiveStatus{Name: "Global Library", Scope: "global"}
	global.EmbeddingStatus, err = library.InspectEmbeddingStatus(ctx, filepath.Join(service.main.Dir, "library"), vector)
	if err := appendArchive(global, err); err != nil {
		return result, err
	}
	paths := make([]string, 0, len(workspaces))
	for _, root := range workspaces {
		paths = append(paths, root)
	}
	sort.Strings(paths)
	for _, root := range paths {
		if err := ctx.Err(); err != nil {
			result.Complete = false
			result.Warnings = append(result.Warnings, err.Error())
			break
		}
		archive := embeddingArchiveStatus{Name: filepath.Base(root), Scope: "workspace", WorkspaceRoot: root, Registered: registered[comparableStudioPath(root)]}
		archive.EmbeddingStatus, err = archiveembed.InspectStatus(ctx, filepath.Join(root, ".q"), vector)
		if err := appendArchive(archive, err); err != nil {
			return result, err
		}
	}
	result.CompletedAt = time.Now().UTC()
	result.SyncJob = service.sessions.embeddingSync.latest()
	return result, publishSnapshot("completed")
}

func appendEmbeddingArchive(result *embeddingStatusResponse, archive embeddingArchiveStatus, config sessionstore.VectorConfig, err error) {
	if err != nil {
		archive.Complete = false
		archive.Issues = append(archive.Issues, err.Error())
	}
	switch {
	case !archive.Complete:
		archive.State = "error"
		result.Complete = false
	case !archive.Exists:
		archive.State = "absent"
	case !config.Enabled():
		archive.State = "disabled"
	case archive.Missing > 0 || archive.Stale > 0 || archive.InvalidVectors > 0 || archive.Index == nil || !archive.Index.ConfigurationMatches || archive.Index.Vectors != archive.CurrentVectors:
		archive.State = "pending"
	default:
		archive.State = "current"
	}
	result.Archives = append(result.Archives, archive)
}
