package app

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
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
	gatewayConfig gateway.Config
	startupErr    error
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
	archiveStore, archiveOpenErr := sessionstore.OpenWithOptions(
		request.workspaceStore.Root,
		sessionstore.OpenOptions{WorkspaceLock: request.workspaceLock},
	)
	result.archiveErr = archiveOpenErr
	if errors.Is(archiveOpenErr, sessionstore.ErrIndexLocked) {
		result.err = archiveOpenErr
		return result
	}

	var archive *sessionstore.Writer
	if archiveOpenErr == nil {
		archive = sessionstore.NewWriter(archiveStore, 0)
	}
	tools, toolsErr := qtools.NewRuntimeWithArchiveAndLoomOptions(
		request.ctx, request.workspaceStore.Root, archiveStore, loaded.LoomStoreOptions(nil),
	)
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
	result.tools = tools
	result.archive = archive

	if request.configErr == nil && request.manager.Endpoint() != "" {
		if !loaded.Provider.Managed {
			loaded.UseManagedGateway()
		}
		configuredClient, clientErr := request.factory(loaded)
		if clientErr != nil {
			result.err = clientErr
			return result
		}
		if models, modelsErr := configuredClient.ListModels(request.ctx); modelsErr == nil {
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
