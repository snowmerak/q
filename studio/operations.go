package studio

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/usagelog"
)

type operationsRuntime interface {
	RuntimeStatus(context.Context) app.RuntimeStatus
	Usage(context.Context, usagelog.Filter) (usagelog.UsageView, error)
}

type operationsService struct {
	store    config.Store
	runtime  operationsRuntime
	sessions *sessionsService
	now      func() time.Time
}

type workerOperationsStatus struct {
	ActiveRuns   int `json:"active_runs"`
	ResidentRuns int `json:"resident_runs"`
}

type retentionOperationsStatus struct {
	HotDays       int    `json:"hot_days"`
	DatabasePath  string `json:"database_path"`
	DatabaseBytes int64  `json:"database_bytes"`
	ArchivePath   string `json:"archive_path"`
	ArchiveFiles  int    `json:"archive_files"`
	ArchiveBytes  int64  `json:"archive_bytes"`
}

type operationsSnapshot struct {
	GeneratedAt      time.Time                  `json:"generated_at"`
	RuntimeAvailable bool                       `json:"runtime_available"`
	Workers          workerOperationsStatus     `json:"workers"`
	Services         []app.RuntimeServiceStatus `json:"services"`
	Usage            usagelog.UsageView         `json:"usage"`
	UsageError       string                     `json:"usage_error,omitempty"`
	Retention        retentionOperationsStatus  `json:"retention"`
	Logs             []string                   `json:"logs"`
}

func newOperationsService(store config.Store, runner sessionRunner, sessions *sessionsService) *operationsService {
	service := &operationsService{store: store, sessions: sessions, now: time.Now}
	service.runtime, _ = runner.(operationsRuntime)
	return service
}

func (service *operationsService) serveSnapshot(writer http.ResponseWriter, request *http.Request) {
	days := 30
	if raw := request.URL.Query().Get("days"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 3650 {
			writeAPIError(writer, http.StatusBadRequest, fmt.Errorf("days must be between 1 and 3650"))
			return
		}
		days = parsed
	}
	now := service.now().UTC()
	active, resident := service.sessions.runs.stats()
	snapshot := operationsSnapshot{
		GeneratedAt: now, RuntimeAvailable: service.runtime != nil,
		Workers:   workerOperationsStatus{ActiveRuns: active, ResidentRuns: resident},
		Retention: usageRetention(service.store.Dir),
	}
	if service.runtime != nil {
		status := service.runtime.RuntimeStatus(request.Context())
		snapshot.Services, snapshot.Logs = status.Services, status.Logs
		usage, err := service.runtime.Usage(request.Context(), usagelog.Filter{From: now.Add(-time.Duration(days) * 24 * time.Hour), To: now})
		if err != nil {
			snapshot.UsageError = err.Error()
		} else {
			snapshot.Usage = usage
		}
	}
	writeJSON(writer, http.StatusOK, snapshot)
}

func usageRetention(configDir string) retentionOperationsStatus {
	root := filepath.Join(configDir, "usage")
	value := retentionOperationsStatus{
		HotDays:      int(usagelog.HotRetention / (24 * time.Hour)),
		DatabasePath: filepath.Join(root, "usage.sqlite"), ArchivePath: filepath.Join(root, "archive"),
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(value.DatabasePath + suffix); err == nil {
			value.DatabaseBytes += info.Size()
		}
	}
	_ = filepath.WalkDir(value.ArchivePath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		value.ArchiveFiles++
		if info, statErr := entry.Info(); statErr == nil {
			value.ArchiveBytes += info.Size()
		}
		return nil
	})
	return value
}
