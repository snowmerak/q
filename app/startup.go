package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/archiveembed"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/sessionstore"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
)

type runtimeInitializedMsg struct {
	config        config.Config
	client        chatClient
	tools         *qtools.Runtime
	archive       *sessionstore.Writer
	archiveSearch *archiveembed.Archive
	library       *qlibrary.Client
	models        []client.Model
	gatewayConfig gateway.Config
	startupErr    error
	mcpErr        error
	mcpStatuses   []qtools.ExternalStatus
	archiveErr    error
	err           error
}

type startupLifecycle struct {
	started   chan struct{}
	done      chan struct{}
	startOnce sync.Once
	doneOnce  sync.Once
	mu        sync.Mutex
	client    chatClient
	tools     *qtools.Runtime
	archive   *sessionstore.Writer
}

func newStartupLifecycle() *startupLifecycle {
	return &startupLifecycle{started: make(chan struct{}), done: make(chan struct{})}
}

func (lifecycle *startupLifecycle) begin() {
	lifecycle.startOnce.Do(func() { close(lifecycle.started) })
}

func (lifecycle *startupLifecycle) finish() {
	lifecycle.doneOnce.Do(func() { close(lifecycle.done) })
}

func (lifecycle *startupLifecycle) setResources(
	configuredClient chatClient,
	tools *qtools.Runtime,
	archive *sessionstore.Writer,
) {
	lifecycle.mu.Lock()
	lifecycle.client = configuredClient
	lifecycle.tools = tools
	lifecycle.archive = archive
	lifecycle.mu.Unlock()
}

func (lifecycle *startupLifecycle) waitIfStarted() {
	select {
	case <-lifecycle.started:
		<-lifecycle.done
	default:
	}
}

func (lifecycle *startupLifecycle) startupClient() chatClient {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	return lifecycle.client
}

func (lifecycle *startupLifecycle) closeResources() error {
	lifecycle.mu.Lock()
	archive := lifecycle.archive
	tools := lifecycle.tools
	lifecycle.archive = nil
	lifecycle.tools = nil
	lifecycle.mu.Unlock()

	var archiveErr error
	if archive != nil {
		archiveErr = archive.Close()
	}
	var toolsErr error
	if tools != nil {
		toolsErr = tools.Close()
	}
	return errors.Join(archiveErr, toolsErr)
}

type startupRequest struct {
	ctx            context.Context
	store          config.Store
	workspaceStore workspace.Store
	workspaceLock  *workspace.Lock
	loaded         config.Config
	configErr      error
	manager        *providerhost.Manager
	factory        clientFactory
	lifecycle      *startupLifecycle
}

func (request startupRequest) run() runtimeInitializedMsg {
	request.lifecycle.begin()
	defer request.lifecycle.finish()

	loaded := request.loaded
	startupErr := request.manager.LoadAndStart(request.ctx)
	if errors.Is(startupErr, providerhost.ErrNotFound) && request.configErr == nil && !loaded.Provider.Managed {
		legacy, legacyErr := providerhost.LegacyProvider(
			"default", loaded.Provider.BaseURL, loaded.Provider.APIKeyEnv, loaded.Provider.APIKey,
		)
		if legacyErr == nil {
			startupErr = request.manager.Apply(request.ctx, gateway.Config{Providers: []gateway.ProviderConfig{legacy}})
			if startupErr == nil {
				if !strings.HasPrefix(loaded.Provider.Model, "default/") {
					loaded.Provider.Model = "default/" + loaded.Provider.Model
				}
				loaded.UseManagedGateway()
				startupErr = request.store.Save(loaded)
			}
		} else {
			startupErr = legacyErr
		}
	} else if errors.Is(startupErr, providerhost.ErrNotFound) {
		startupErr = nil
	}

	result := runtimeInitializedMsg{
		config: loaded, gatewayConfig: request.manager.Config(), startupErr: startupErr,
	}
	vectorConfig := sessionstore.VectorConfig{}
	if loaded.Embedding.Model != "" {
		vectorConfig = sessionstore.VectorConfig{
			Model: loaded.Embedding.Model, Dimensions: loaded.Embedding.Dimensions,
		}
	}
	archiveStore, archiveOpenErr := sessionstore.OpenWithOptions(
		request.workspaceStore.Root,
		sessionstore.OpenOptions{WorkspaceLock: request.workspaceLock, Vector: vectorConfig},
	)
	result.archiveErr = archiveOpenErr
	if errors.Is(archiveOpenErr, sessionstore.ErrIndexLocked) {
		result.err = archiveOpenErr
		return result
	}

	var archive *sessionstore.Writer
	var semanticArchive *archiveembed.Archive
	if archiveOpenErr == nil {
		semanticArchive = archiveembed.New(archiveStore)
		archive = sessionstore.NewWriterWithOptions(archiveStore, sessionstore.WriterOptions{
			Context: request.ctx, Prepare: semanticArchive.Prepare,
		})
	}
	workspaceLSP, toolsErr := request.workspaceStore.LoadLSP()
	var tools *qtools.Runtime
	var libraryClient *qlibrary.Client
	if toolsErr == nil {
		libraryConfig, libraryConfigErr := (qlibrary.ConfigStore{Dir: request.store.Dir}).LoadOrDefault()
		if libraryConfigErr != nil {
			libraryConfig = qlibrary.DefaultConfig()
		}
		libraryClient = qlibrary.NewClient(libraryConfig.Endpoint(), libraryConfig.ResolveAPIKey(), 5*time.Second)
		tools, toolsErr = qtools.NewRuntimeWithArchiveAndLoomOptionsAndLSPAndLibrary(
			request.ctx, request.workspaceStore.Root, semanticArchive, loaded.LoomStoreOptions(nil), loaded.LSP, workspaceLSP, libraryClient,
		)
		result.tools = tools
		result.library = libraryClient
		if toolsErr == nil {
			mcpValue, mcpErr := (mcpconfig.Store{Dir: request.store.Dir}).LoadOrDefault()
			result.mcpErr = mcpErr
			if mcpErr == nil {
				result.mcpStatuses = tools.ConfigureExternal(request.ctx, request.workspaceStore.Root, mcpValue)
			}
		}
	}
	if toolsErr != nil {
		if archive != nil {
			_ = archive.Close()
		} else if archiveStore != nil {
			_ = archiveStore.Close()
		}
		result.err = toolsErr
		return result
	}
	request.lifecycle.setResources(nil, tools, archive)
	result.archive = archive
	result.archiveSearch = semanticArchive

	if request.configErr == nil && request.manager.Endpoint() != "" {
		if !loaded.Provider.Managed {
			loaded.UseManagedGateway()
		}
		configuredClient, clientErr := request.factory(loaded)
		if clientErr != nil {
			result.err = clientErr
			return result
		}
		if loaded.Embedding.Model != "" && libraryClient != nil {
			embedder, ok := configuredClient.(qlibrary.Embedder)
			if !ok {
				_ = configuredClient.Close()
				result.err = errors.New("configured LLM client does not support embeddings")
				return result
			}
			if embeddingErr := libraryClient.ConfigureEmbedding(
				embedder, loaded.Embedding.Model, loaded.Embedding.Dimensions,
			); embeddingErr != nil {
				_ = configuredClient.Close()
				result.err = embeddingErr
				return result
			}
			if semanticArchive != nil {
				if embeddingErr := semanticArchive.Configure(
					embedder, loaded.Embedding.Model, loaded.Embedding.Dimensions,
				); embeddingErr != nil {
					_ = configuredClient.Close()
					result.err = embeddingErr
					return result
				}
				go func() { _, _ = semanticArchive.Backfill(request.ctx) }()
			}
		}
		if models, modelsErr := configuredClient.ListModels(request.ctx); modelsErr == nil {
			result.models = append([]client.Model(nil), models...)
			refreshed, found := refreshModelContextWindow(loaded, models)
			if found && refreshed.Provider.ContextWindow != loaded.Provider.ContextWindow {
				loaded = refreshed
				_ = request.store.Save(loaded)
			}
		}
		request.lifecycle.setResources(configuredClient, tools, archive)
		result.config = loaded
		result.client = configuredClient
	}
	return result
}
