// Package app assembles the single-instance q v2 core engine, storage
// backends, LLM/Decisions routers, and HTTP/SSE control surface shared by
// cmd/q and the Wails v3 desktop shell.
package app

import (
	"context"
	"encoding/json"
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

type managedGateway struct {
	gw        *gateway.Gateway
	refs      atomic.Int64
	closeOnce sync.Once
}

func newManagedGateway(gw *gateway.Gateway) *managedGateway {
	mg := &managedGateway{gw: gw}
	mg.refs.Store(1)
	return mg
}

func (m *managedGateway) Acquire() bool {
	if m == nil || m.gw == nil {
		return false
	}
	for {
		cur := m.refs.Load()
		if cur <= 0 {
			return false
		}
		if m.refs.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

func (m *managedGateway) Release() {
	if m == nil {
		return
	}
	if m.refs.Add(-1) == 0 {
		m.closeOnce.Do(func() {
			if m.gw != nil {
				_ = m.gw.Close()
			}
		})
	}
}

type managedChatCompleter struct {
	mg *managedGateway
}

func (c managedChatCompleter) Chat(ctx context.Context, req llmprovider.ChatRequest) (*llmprovider.ChatResponse, error) {
	if !c.mg.Acquire() {
		return nil, errors.New("app: gateway closed")
	}
	defer c.mg.Release()
	return c.mg.gw.Chat(ctx, req)
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

	reloadMu             sync.Mutex
	providersInitialized bool
	lastCfg              config.Config
	llmDegraded          string
	decDegraded          string
	embDegraded          string
	mcpDegraded          string

	providerMu     sync.RWMutex
	gateway        *managedGateway
	decisionsReg   *decisions.Registry
	embedder       appEmbedder
	degradedReason string

	reindexCtx     context.Context
	reindexCancel  context.CancelFunc
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

	reindexCtx, reindexCancel := context.WithCancel(context.Background())
	a := &App{
		dataDir:       dataDir,
		configPath:    filepath.Join(dataDir, config.FileName),
		logger:        logger,
		lock:          lock,
		reindexCtx:    reindexCtx,
		reindexCancel: reindexCancel,
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
	a.loom = task.NewLoomStoreWithOptions(a.rdb, a.blob, loomOptionsFromConfig(cfg))
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
	a.loom.StartGCWorker(ctx, 15*time.Minute, func(err error) {
		a.logger.Warn("loom gc worker error", "error", err)
	})

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
	if err := a.engine.RecoverOrphanTurns(ctx); err != nil {
		a.logger.Warn("orphan turn recovery failed", "error", err)
	}

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

func sameJSONConfig(a, b any) bool {
	ba, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ba) == string(bb)
}

func (a *App) reloadProviders(ctx context.Context, cfg config.Config) {
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()

	a.providerMu.RLock()
	curMG := a.gateway
	curDec := a.decisionsReg
	curEmb := a.embedder
	a.providerMu.RUnlock()

	prevInit := a.providersInitialized
	prevCfg := a.lastCfg

	llmNeedsRebuild := !prevInit || !sameJSONConfig(prevCfg.GatewayConfig(), cfg.GatewayConfig()) || a.llmDegraded != ""
	decNeedsRebuild := !prevInit || !sameJSONConfig(prevCfg.Decisions, cfg.Decisions) || a.decDegraded != ""
	mcpNeedsReconfig := !prevInit || !sameJSONConfig(prevCfg.Tools.MCPServers, cfg.Tools.MCPServers) || a.mcpDegraded != ""

	nextMG := curMG
	llmDegraded := a.llmDegraded
	gatewayChanged := false

	if llmNeedsRebuild {
		llmDegraded = ""
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
				llmDegraded = fmt.Sprintf("llm: %v", err)
				nextMG = nil
			} else {
				nextMG = newManagedGateway(built)
			}
		} else {
			nextMG = nil
		}
		gatewayChanged = nextMG != curMG
	}

	nextDec := curDec
	decDegraded := a.decDegraded
	if decNeedsRebuild {
		decDegraded = ""
		nextDec = nil
		if len(cfg.Decisions.Models) > 0 || len(cfg.Decisions.Providers) > 0 {
			reg, err := decisions.NewRegistry(cfg.Decisions, os.Getenv)
			if err != nil {
				decDegraded = fmt.Sprintf("decisions: %v", err)
			} else {
				nextDec = reg
				warmCtx, cancelWarm := context.WithTimeout(ctx, 10*time.Second)
				_ = nextDec.RefreshModels(warmCtx)
				cancelWarm()
			}
		}
	}

	embNeedsRebuild := !prevInit || gatewayChanged || !sameJSONConfig(prevCfg.Embeddings, cfg.Embeddings) || a.embDegraded != ""
	nextEmb := curEmb
	embDegraded := a.embDegraded
	if embNeedsRebuild {
		embDegraded = ""
		nextEmb = nil
		if cfg.Embeddings.Enabled {
			if pid := strings.TrimSpace(cfg.Embeddings.ProviderID); pid != "" {
				var found *gateway.ProviderConfig
				for i := range cfg.LLM.Providers {
					if strings.TrimSpace(cfg.LLM.Providers[i].ID) == pid {
						found = &cfg.LLM.Providers[i]
						break
					}
				}
				switch {
				case found == nil:
					embDegraded = fmt.Sprintf("embeddings: provider %q not found", pid)
				case !found.Enabled || nextMG == nil:
					embDegraded = fmt.Sprintf("embeddings: provider %q is disabled or gateway unavailable", pid)
				default:
					builtEmb, err := newGatewayEmbedder(nextMG, *found, cfg.Embeddings)
					if err != nil {
						embDegraded = fmt.Sprintf("embeddings: %v", err)
					} else {
						nextEmb = builtEmb
					}
				}
			} else {
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
					embDegraded = fmt.Sprintf("embeddings: %v", err)
				} else {
					nextEmb = builtEmb
				}
			}
		}
	}

	mcpDegraded := a.mcpDegraded
	if mcpNeedsReconfig && a.mcp != nil {
		var mcpMsgs []string
		for _, st := range a.mcp.Configure(ctx, cfg.Tools.MCPServers) {
			if st.Error != "" {
				mcpMsgs = append(mcpMsgs, fmt.Sprintf("mcp(%s): %s", st.ID, st.Error))
			}
		}
		mcpDegraded = strings.Join(mcpMsgs, "; ")
	}

	var degradedParts []string
	for _, part := range []string{llmDegraded, decDegraded, embDegraded, mcpDegraded} {
		if part != "" {
			degradedParts = append(degradedParts, part)
		}
	}

	a.providerMu.Lock()
	oldMG := a.gateway
	a.gateway = nextMG
	a.decisionsReg = nextDec
	a.embedder = nextEmb
	a.degradedReason = strings.Join(degradedParts, "; ")
	a.providerMu.Unlock()

	a.providersInitialized = true
	a.lastCfg = cfg
	a.llmDegraded = llmDegraded
	a.decDegraded = decDegraded
	a.embDegraded = embDegraded
	a.mcpDegraded = mcpDegraded

	if a.outbox != nil && embNeedsRebuild {
		if nextEmb != nil {
			a.outbox.SetEmbedder(nextEmb)
		} else {
			a.outbox.SetEmbedder(nil)
		}
	}
	if a.loom != nil {
		a.loom.Configure(loomOptionsFromConfig(cfg))
	}
	if oldMG != nil && oldMG != nextMG {
		oldMG.Release()
	}
}

func loomOptionsFromConfig(cfg config.Config) task.LoomOptions {
	blob := cfg.Storage.Blob
	return task.LoomOptions{
		MaxArtifactBytes: blob.LoomMaxArtifactBytes,
		MaxStoreBytes:    blob.LoomMaxStoreBytes,
		AutoGC:           !blob.LoomGC.Disabled,
		GCTriggerRatio:   blob.LoomGC.TriggerRatio,
		GCTargetRatio:    blob.LoomGC.TargetRatio,
		GCGracePeriod:    time.Duration(blob.LoomGC.GraceHours) * time.Hour,
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
// Callers must invoke Snapshot.Close() when done.
func (a *App) Snapshot() runtime.ProviderSnapshot {
	cfg := a.currentConfig()
	a.providerMu.RLock()
	mg := a.gateway
	dec := a.decisionsReg
	emb := a.embedder
	a.providerMu.RUnlock()

	var gw *gateway.Gateway
	var release func()
	if mg != nil && mg.Acquire() {
		gw = mg.gw
		var once sync.Once
		release = func() { once.Do(mg.Release) }
	}

	var qEmb memory.QueryEmbedder
	if emb != nil {
		qEmb = emb
	}
	return runtime.ProviderSnapshot{
		Config:    cfg,
		Gateway:   gw,
		Decisions: dec,
		Embedder:  qEmb,
		Release:   release,
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
	a.providerMu.RLock()
	mg := a.gateway
	a.providerMu.RUnlock()
	if !mg.Acquire() {
		return nil, "", "", 0, errors.New("app: no LLM gateway configured")
	}
	defer mg.Release()
	cfg := a.currentConfig()
	for _, m := range members {
		if m.CharacterID == characterID {
			r := runtime.ResolveModels(ctx, cfg, room, m.CharacterSnapshot, mg.gw)
			return managedChatCompleter{mg: mg}, r.MemoryModel, r.MemoryReasoningEffort, r.ContextWindow, nil
		}
	}
	return nil, "", "", 0, fmt.Errorf("app: character %q not found in room %q", characterID, roomID)
}

// StartAsync binds the HTTP listener synchronously, launches the server in a
// background goroutine tied to ctx, and returns the bound address and error channel.
func (a *App) StartAsync(ctx context.Context) (string, <-chan error, error) {
	ln, err := net.Listen("tcp", a.listenAddr)
	if err != nil {
		return "", nil, fmt.Errorf("listen %s: %w", a.listenAddr, err)
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
		serveErrCh := make(chan error, 1)
		go func() {
			if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				serveErrCh <- serveErr
				return
			}
			serveErrCh <- nil
		}()

		select {
		case err := <-serveErrCh:
			errCh <- err
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
			errCh <- <-serveErrCh
		}
	}()

	return ln.Addr().String(), errCh, nil
}

// Start binds the HTTP listener and blocks until ctx is cancelled or the server stops.
func (a *App) Start(ctx context.Context) error {
	_, errCh, err := a.StartAsync(ctx)
	if err != nil {
		return err
	}
	return <-errCh
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
	mg := a.gateway
	a.providerMu.RUnlock()
	if mg == nil {
		return nil
	}
	return mg.gw
}

func (a *App) currentConfig() config.Config {
	if a.watcher != nil {
		return a.watcher.Current()
	}
	return config.Default()
}

func (a *App) listModels(ctx context.Context) ([]llmprovider.Model, error) {
	a.providerMu.RLock()
	mg := a.gateway
	a.providerMu.RUnlock()
	if !mg.Acquire() {
		return []llmprovider.Model{}, nil
	}
	defer mg.Release()
	return mg.gw.Models(ctx)
}

func (a *App) refreshModels(ctx context.Context) ([]llmprovider.Model, error) {
	a.providerMu.RLock()
	mg := a.gateway
	dec := a.decisionsReg
	a.providerMu.RUnlock()
	if dec != nil {
		_ = dec.RefreshModels(ctx)
	}
	if !mg.Acquire() {
		return []llmprovider.Model{}, nil
	}
	defer mg.Release()
	return mg.gw.RefreshModels(ctx)
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
		if a.loom != nil {
			a.loom.StopGCWorker()
		}
		if a.reindexCancel != nil {
			a.reindexCancel()
		}
		a.reindexWG.Wait()
		if a.outbox != nil {
			a.outbox.Stop()
		}
		if a.watcher != nil {
			closeErr = errors.Join(closeErr, a.watcher.Close())
		}
		a.providerMu.Lock()
		mg := a.gateway
		a.gateway = nil
		a.providerMu.Unlock()
		if mg != nil {
			mg.Release()
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
