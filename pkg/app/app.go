// Package app assembles the single-instance q v2 core engine, storage
// backends, LLM/Decisions routers, and HTTP/SSE control surface shared by
// cmd/q and the Wails v3 desktop shell.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/v2/internal/fsutil"
	"github.com/snowmerak/q/v2/pkg/app/config"
	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
	"github.com/snowmerak/q/v2/pkg/llm/decisions"
	"github.com/snowmerak/q/v2/pkg/llm/gateway"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/blob/localfs"
	"github.com/snowmerak/q/v2/pkg/storage/outbox"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/sqlite"
	"github.com/snowmerak/q/v2/pkg/storage/search/blevehnsw"
)

type Options struct {
	DataDir        string
	ListenOverride string
	Logger         *slog.Logger
}

type App struct {
	dataDir    string
	configPath string
	listenAddr string
	logger     *slog.Logger

	lock    *fsutil.InstanceLock
	watcher *config.Watcher

	rdb    storage.RDBStore
	search storage.SearchIndex
	blob   storage.BlobStore
	outbox *outbox.Worker

	providerMu     sync.RWMutex
	gateway        *gateway.Gateway
	decisionsReg   *decisions.Registry
	degradedReason string

	serverMu  sync.Mutex
	listener  net.Listener
	server    *http.Server
	closeOnce sync.Once
}

// Open initializes the single-instance q v2 engine using Options.
// If any acquisition step fails, previously acquired resources are released.
func Open(ctx context.Context, opts Options) (*App, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dataDir, err := config.ResolveDataDir(opts.DataDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", dataDir, err)
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}

	lock, err := fsutil.AcquireInstanceLock(filepath.Join(dataDir, "instance.lock"))
	if err != nil {
		return nil, err
	}

	a := &App{
		dataDir:    dataDir,
		configPath: filepath.Join(dataDir, config.FileName),
		logger:     logger,
		lock:       lock,
	}

	cleanupOnErr := true
	defer func() {
		if cleanupOnErr {
			_ = a.Close(context.Background())
		}
	}()

	cfg, err := config.Load(a.configPath)
	if err != nil {
		return nil, err
	}
	listen := cfg.Server.Listen
	if strings.TrimSpace(opts.ListenOverride) != "" {
		listen = strings.TrimSpace(opts.ListenOverride)
	}
	a.listenAddr = listen

	if err := a.openStorage(ctx, cfg); err != nil {
		return nil, err
	}
	a.reloadProviders(ctx, cfg)

	a.outbox = outbox.NewWorker(a.rdb, a.search, nil, outbox.WithErrorHandler(func(err error) {
		a.logger.Warn("outbox worker error", "error", err)
	}))
	a.outbox.Start(ctx)

	watcher, err := config.StartWatcher(ctx, a.configPath, cfg, func(prev, next config.Config) {
		if restartFields := config.DiffRestartRequired(prev, next); len(restartFields) > 0 {
			a.logger.Warn("config reload contains fields requiring restart", "fields", restartFields)
		}
		a.reloadProviders(context.Background(), next)
	}, func(err error) {
		a.logger.Warn("config reload ignored invalid candidate", "error", err)
	})
	if err != nil {
		return nil, fmt.Errorf("start config watcher: %w", err)
	}
	a.watcher = watcher

	cleanupOnErr = false
	return a, nil
}

func (a *App) openStorage(ctx context.Context, cfg config.Config) error {
	switch cfg.Storage.RDB.Driver {
	case "sqlite":
		dbPath := cfg.Storage.RDB.SQLitePath
		if !filepath.IsAbs(dbPath) {
			dbPath = filepath.Join(a.dataDir, dbPath)
		}
		rdb, err := sqlite.Open(ctx, dbPath)
		if err != nil {
			return fmt.Errorf("open sqlite rdb: %w", err)
		}
		a.rdb = rdb
	default:
		return fmt.Errorf("rdb driver %q is not yet implemented in this milestone", cfg.Storage.RDB.Driver)
	}

	switch cfg.Storage.Search.Driver {
	case "bleve":
		idx, err := blevehnsw.Open(filepath.Join(a.dataDir, "index"))
		if err != nil {
			return fmt.Errorf("open blevehnsw search: %w", err)
		}
		a.search = idx
	default:
		return fmt.Errorf("search driver %q is not yet implemented in this milestone", cfg.Storage.Search.Driver)
	}

	switch cfg.Storage.Blob.Driver {
	case "local":
		bs, err := localfs.Open(filepath.Join(a.dataDir, "blobs"))
		if err != nil {
			return fmt.Errorf("open localfs blob: %w", err)
		}
		a.blob = bs
	default:
		return fmt.Errorf("blob driver %q is not yet implemented in this milestone", cfg.Storage.Blob.Driver)
	}
	return nil
}

func (a *App) reloadProviders(ctx context.Context, cfg config.Config) {
	var gw *gateway.Gateway
	var degraded []string

	hasEnabledLLM := false
	for _, p := range cfg.LLM.Providers {
		if p.Enabled {
			hasEnabledLLM = true
			break
		}
	}
	if hasEnabledLLM {
		built, err := gateway.NewContext(ctx, cfg.GatewayConfig())
		if err != nil {
			degraded = append(degraded, fmt.Sprintf("llm: %v", err))
		} else {
			gw = built
		}
	}

	decReg, err := decisions.NewRegistry(cfg.Decisions, os.Getenv)
	if err != nil {
		degraded = append(degraded, fmt.Sprintf("decisions: %v", err))
	}

	a.providerMu.Lock()
	oldGW := a.gateway
	a.gateway = gw
	a.decisionsReg = decReg
	a.degradedReason = strings.Join(degraded, "; ")
	a.providerMu.Unlock()

	if oldGW != nil {
		_ = oldGW.Close()
	}
}

// Start binds the HTTP listener and blocks until ctx is cancelled or the server stops.
func (a *App) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", a.listenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", a.listenAddr, err)
	}
	srv := &http.Server{
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	a.serverMu.Lock()
	a.listener = ln
	a.server = srv
	a.serverMu.Unlock()

	errCh := make(chan error, 1)
	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return <-errCh
	}
}

// Addr returns the bound listener address after Start, or the configured address before Start.
func (a *App) Addr() string {
	a.serverMu.Lock()
	ln := a.listener
	a.serverMu.Unlock()
	if ln != nil {
		return ln.Addr().String()
	}
	return a.listenAddr
}

func (a *App) DataDir() string { return a.dataDir }

func (a *App) currentConfig() config.Config {
	if a.watcher != nil {
		return a.watcher.Current()
	}
	return config.Default()
}

func (a *App) listModels(ctx context.Context) ([]llmprovider.Model, error) {
	a.providerMu.RLock()
	gw := a.gateway
	a.providerMu.RUnlock()
	if gw == nil {
		return []llmprovider.Model{}, nil
	}
	return gw.Models(ctx)
}

func (a *App) refreshModels(ctx context.Context) ([]llmprovider.Model, error) {
	a.providerMu.RLock()
	gw := a.gateway
	a.providerMu.RUnlock()
	if gw == nil {
		return []llmprovider.Model{}, nil
	}
	return gw.RefreshModels(ctx)
}

func (a *App) listDecisionModels() ([]decisions.ModelInfo, string) {
	a.providerMu.RLock()
	reg := a.decisionsReg
	a.providerMu.RUnlock()
	if reg == nil {
		return []decisions.ModelInfo{}, ""
	}
	models := reg.ListModels()
	if models == nil {
		models = []decisions.ModelInfo{}
	}
	return models, reg.DefaultModelID()
}

// Close shuts down the HTTP server, background workers, storage backends, and instance lock.
func (a *App) Close(ctx context.Context) error {
	var closeErr error
	a.closeOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		a.serverMu.Lock()
		srv := a.server
		a.server = nil
		a.serverMu.Unlock()
		if srv != nil {
			closeErr = errors.Join(closeErr, srv.Shutdown(ctx))
		}
		if a.outbox != nil {
			a.outbox.Stop()
		}
		if a.watcher != nil {
			closeErr = errors.Join(closeErr, a.watcher.Close())
		}
		a.providerMu.Lock()
		gw := a.gateway
		a.gateway = nil
		a.providerMu.Unlock()
		if gw != nil {
			closeErr = errors.Join(closeErr, gw.Close())
		}
		if a.search != nil {
			closeErr = errors.Join(closeErr, a.search.Close())
		}
		if a.rdb != nil {
			closeErr = errors.Join(closeErr, a.rdb.Close())
		}
		if a.blob != nil {
			closeErr = errors.Join(closeErr, a.blob.Close())
		}
		if a.lock != nil {
			closeErr = errors.Join(closeErr, a.lock.Release())
		}
	})
	return closeErr
}
