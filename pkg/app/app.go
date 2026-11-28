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
	"sync/atomic"
	"time"

	"github.com/snowmerak/q/v2/internal/fsutil"
	"github.com/snowmerak/q/v2/pkg/app/config"
	"github.com/snowmerak/q/v2/pkg/knowledge"
	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
	"github.com/snowmerak/q/v2/pkg/llm/decisions"
	"github.com/snowmerak/q/v2/pkg/llm/gateway"
	"github.com/snowmerak/q/v2/pkg/memory"
	"github.com/snowmerak/q/v2/pkg/runtime"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/blob/localfs"
	"github.com/snowmerak/q/v2/pkg/storage/blob/s3"
	"github.com/snowmerak/q/v2/pkg/storage/outbox"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/mysql"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/postgres"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/sqlite"
	"github.com/snowmerak/q/v2/pkg/storage/search/blevehnsw"
	"github.com/snowmerak/q/v2/pkg/storage/search/opensearch"
	"github.com/snowmerak/q/v2/pkg/task"
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

	lock        *fsutil.InstanceLock
	watcher     *config.Watcher
	rdb         storage.RDBStore
	search      storage.SearchIndex
	blob        storage.BlobStore
	outbox      *outbox.Worker
	valueWorker *memory.ValueWorker
	knowledge   *knowledge.Service
	loom        *task.LoomStore
	commands    *task.CommandManager
	skills      *task.SkillStore
	mcp         *task.MCPManager
	browser     *task.BrowserController
	tools       *task.Registry
	engine      *runtime.Engine

	providerMu     sync.RWMutex
	gateway        *gateway.Gateway
	decisionsReg   *decisions.Registry
	embedder       *outbox.HTTPEmbedder
	degradedReason string

	reindexRunning atomic.Bool
	reindexWG      sync.WaitGroup

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

	a.outbox = outbox.NewWorker(a.rdb, a.search, nil, outbox.WithErrorHandler(func(err error) {
		a.logger.Warn("outbox worker error", "error", err)
	}))
	a.knowledge = knowledge.NewService(a.rdb, a.search, a.currentQueryEmbedder)
	a.loom = task.NewLoomStore(a.rdb, a.blob, cfg.Storage.Blob.LoomMaxArtifactBytes)
	a.commands = task.NewCommandManager(time.Duration(cfg.Tools.CommandTimeoutMS) * time.Millisecond)
	a.skills = task.NewSkillStore(filepath.Join(a.dataDir, "skills"))
	a.mcp = task.NewMCPManager()
	a.browser = task.NewBrowserController(a.currentBrowserControl, nil)
	a.tools = task.NewRegistry(task.RegistryDeps{
		RDB:        a.rdb,
		Search:     a.search,
		Loom:       a.loom,
		Commands:   a.commands,
		Skills:     a.skills,
		MCP:        a.mcp,
		Browser:    a.browser,
		EmbedderFn: a.currentQueryEmbedder,
	})

	a.reloadProviders(ctx, cfg)
	a.outbox.Start(ctx)

	a.valueWorker = memory.NewValueWorker(a.rdb, a.resolveValueModel, memory.WithValueErrorHandler(func(err error) {
		a.logger.Warn("value worker error", "error", err)
	}))
	a.valueWorker.Start(ctx)

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

	a.engine = runtime.NewEngine(a.rdb, a.search, a, a.logger)
	a.engine.SetTools(a.tools, a.dataDir)

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
	case "postgres":
		dsnEnv := strings.TrimSpace(cfg.Storage.RDB.DSNEnv)
		dsn := ""
		if dsnEnv != "" {
			dsn = strings.TrimSpace(os.Getenv(dsnEnv))
		}
		if dsn == "" {
			return fmt.Errorf("postgres rdb requires non-empty env var %q", dsnEnv)
		}
		rdb, err := postgres.Open(ctx, dsn)
		if err != nil {
			return fmt.Errorf("open postgres rdb: %w", err)
		}
		a.rdb = rdb
	case "mysql":
		dsnEnv := strings.TrimSpace(cfg.Storage.RDB.DSNEnv)
		dsn := ""
		if dsnEnv != "" {
			dsn = strings.TrimSpace(os.Getenv(dsnEnv))
		}
		if dsn == "" {
			return fmt.Errorf("mysql rdb requires non-empty env var %q", dsnEnv)
		}
		rdb, err := mysql.Open(ctx, dsn)
		if err != nil {
			return fmt.Errorf("open mysql rdb: %w", err)
		}
		a.rdb = rdb
	default:
		return fmt.Errorf("unsupported rdb driver %q", cfg.Storage.RDB.Driver)
	}

	switch cfg.Storage.Search.Driver {
	case "bleve":
		idx, err := blevehnsw.Open(filepath.Join(a.dataDir, "index"))
		if err != nil {
			return fmt.Errorf("open blevehnsw search: %w", err)
		}
		a.search = idx
	case "opensearch":
		osCfg := cfg.Storage.Search.OpenSearch
		var password, bearer string
		if env := strings.TrimSpace(osCfg.PasswordEnv); env != "" {
			password = os.Getenv(env)
		}
		if env := strings.TrimSpace(osCfg.BearerTokenEnv); env != "" {
			bearer = os.Getenv(env)
		}
		idx, err := opensearch.Open(ctx, opensearch.Config{
			Endpoint:    osCfg.Endpoint,
			IndexPrefix: osCfg.IndexPrefix,
			Username:    osCfg.Username,
			Password:    password,
			BearerToken: bearer,
			Timeout:     time.Duration(osCfg.TimeoutMS) * time.Millisecond,
		})
		if err != nil {
			return fmt.Errorf("open opensearch search: %w", err)
		}
		a.search = idx
	default:
		return fmt.Errorf("unsupported search driver %q", cfg.Storage.Search.Driver)
	}

	switch cfg.Storage.Blob.Driver {
	case "local":
		bs, err := localfs.Open(filepath.Join(a.dataDir, "blobs"))
		if err != nil {
			return fmt.Errorf("open localfs blob: %w", err)
		}
		a.blob = bs
	case "s3":
		s3Cfg := cfg.Storage.Blob.S3
		var accessKey, secretKey string
		if env := strings.TrimSpace(s3Cfg.AccessKeyEnv); env != "" {
			accessKey = os.Getenv(env)
		}
		if env := strings.TrimSpace(s3Cfg.SecretKeyEnv); env != "" {
			secretKey = os.Getenv(env)
		}
		bs, err := s3.Open(ctx, s3.Config{
			Endpoint:     s3Cfg.Endpoint,
			Bucket:       s3Cfg.Bucket,
			Region:       s3Cfg.Region,
			AccessKey:    accessKey,
			SecretKey:    secretKey,
			UsePathStyle: strings.TrimSpace(s3Cfg.Endpoint) != "",
		})
		if err != nil {
			return fmt.Errorf("open s3 blob: %w", err)
		}
		a.blob = bs
	default:
		return fmt.Errorf("unsupported blob driver %q", cfg.Storage.Blob.Driver)
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

	var decReg *decisions.Registry
	if len(cfg.Decisions.Models) > 0 || len(cfg.Decisions.Providers) > 0 {
		reg, err := decisions.NewRegistry(cfg.Decisions, os.Getenv)
		if err != nil {
			degraded = append(degraded, fmt.Sprintf("decisions: %v", err))
		} else {
			decReg = reg
		}
	}

	var emb *outbox.HTTPEmbedder
	if cfg.Embeddings.Enabled {
		builtEmb, err := outbox.NewHTTPEmbedder(outbox.HTTPEmbedderConfig{
			BaseURL:        cfg.Embeddings.BaseURL,
			APIKeyEnv:      cfg.Embeddings.APIKeyEnv,
			Model:          cfg.Embeddings.Model,
			Dimensions:     cfg.Embeddings.Dimensions,
			QueryPrefix:    cfg.Embeddings.QueryPrefix,
			DocumentPrefix: cfg.Embeddings.DocumentPrefix,
			Timeout:        time.Duration(cfg.Embeddings.TimeoutMS) * time.Millisecond,
			LookupEnv:      os.Getenv,
		})
		if err != nil {
			degraded = append(degraded, fmt.Sprintf("embeddings: %v", err))
		} else {
			emb = builtEmb
		}
	}

	if a.mcp != nil {
		for _, st := range a.mcp.Configure(ctx, cfg.Tools.MCPServers) {
			if st.Error != "" {
				degraded = append(degraded, fmt.Sprintf("mcp(%s): %s", st.ID, st.Error))
			}
		}
	}

	a.providerMu.Lock()
	oldGW := a.gateway
	a.gateway = gw
	a.decisionsReg = decReg
	a.embedder = emb
	a.degradedReason = strings.Join(degraded, "; ")
	a.providerMu.Unlock()

	if a.outbox != nil {
		if emb != nil {
			a.outbox.SetEmbedder(emb)
		} else {
			a.outbox.SetEmbedder(nil)
		}
	}
	if oldGW != nil {
		_ = oldGW.Close()
	}
}

func (a *App) currentQueryEmbedder() memory.QueryEmbedder {
	a.providerMu.RLock()
	emb := a.embedder
	a.providerMu.RUnlock()
	if emb == nil {
		return nil
	}
	return emb
}

func (a *App) currentBrowserControl() config.BrowserControlConfig {
	return a.currentConfig().Tools.BrowserControl
}

// Snapshot satisfies runtime.ProviderSource with the current hot-reloadable backends.
func (a *App) Snapshot() runtime.ProviderSnapshot {
	cfg := a.currentConfig()
	a.providerMu.RLock()
	gw := a.gateway
	dec := a.decisionsReg
	emb := a.embedder
	a.providerMu.RUnlock()

	var qEmb memory.QueryEmbedder
	if emb != nil {
		qEmb = emb
	}
	return runtime.ProviderSnapshot{
		Config:    cfg,
		Gateway:   gw,
		Decisions: dec,
		Embedder:  qEmb,
	}
}

func (a *App) resolveValueModel(ctx context.Context, roomID, characterID string) (memory.ChatCompleter, string, string, int, error) {
	room, err := a.rdb.Rooms().Get(ctx, roomID)
	if err != nil {
		return nil, "", "", 0, err
	}
	members, err := a.rdb.Rooms().ListMembers(ctx, roomID)
	if err != nil {
		return nil, "", "", 0, err
	}
	snap := a.Snapshot()
	if snap.Gateway == nil {
		return nil, "", "", 0, errors.New("app: no LLM gateway configured")
	}
	for _, m := range members {
		if m.CharacterID == characterID {
			r := runtime.ResolveModels(ctx, snap.Config, room, m.CharacterSnapshot, snap.Gateway)
			return snap.Gateway, r.MemoryModel, r.MemoryReasoningEffort, r.ContextWindow, nil
		}
	}
	return nil, "", "", 0, fmt.Errorf("app: character %q not found in room %q", characterID, roomID)
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

func (a *App) DataDir() string         { return a.dataDir }
func (a *App) Engine() *runtime.Engine { return a.engine }
func (a *App) RDB() storage.RDBStore   { return a.rdb }

func (a *App) Gateway() *gateway.Gateway {
	a.providerMu.RLock()
	gw := a.gateway
	a.providerMu.RUnlock()
	return gw
}

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

// Close shuts down the HTTP server, runtime engine, tools, background workers, storage backends, and instance lock.
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
		if a.engine != nil {
			a.engine.Close()
		}
		if a.commands != nil {
			a.commands.Close()
		}
		if a.mcp != nil {
			a.mcp.Close()
		}
		if a.valueWorker != nil {
			a.valueWorker.Stop()
		}
		a.reindexWG.Wait()
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
