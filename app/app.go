// Package app implements q's interactive Bubble Tea application.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/providerhost"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
	"github.com/snowmerak/q/workspacememory"
)

type chatClient interface {
	Chat(context.Context, client.ChatRequest) (*client.ChatResponse, error)
	ListModels(context.Context) ([]client.Model, error)
	Close() error
}

type clientFactory func(config.Config) (chatClient, error)

type providerRuntime interface {
	Endpoint() string
	APIKey() string
	Config() gateway.Config
	Apply(context.Context, gateway.Config) error
}

type agentToolRuntime interface {
	Tools() []client.Tool
	Environment() qtools.HostEnvironment
	Call(context.Context, client.ToolCall) (client.ToolResult, error)
}

func defaultClientFactory(value config.Config) (chatClient, error) {
	apiKey := value.Provider.ResolveAPIKey()
	return client.New(client.Config{
		BaseURL:       value.Provider.BaseURL,
		APIKey:        apiKey,
		DefaultModel:  value.Provider.Model,
		DisableAPIKey: apiKey == "",
	})
}

func managedClientFactory(runtime providerRuntime) clientFactory {
	return func(value config.Config) (chatClient, error) {
		endpoint := runtime.Endpoint()
		if endpoint == "" {
			return nil, errors.New("internal LLM Gateway is not running")
		}
		apiKey := runtime.APIKey()
		if apiKey == "" {
			return nil, errors.New("internal LLM Gateway API key is unavailable")
		}
		return client.New(client.Config{
			BaseURL:      endpoint,
			APIKey:       apiKey,
			DefaultModel: value.Provider.Model,
		})
	}
}

// Run loads personal configuration and starts the interactive application.
// A missing configuration opens the first-run provider setup screen.
func Run(ctx context.Context, store config.Store) error {
	runtimeContext, cancelRuntime := context.WithCancel(ctx)
	defer cancelRuntime()
	providerContext, cancelProvider := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelProvider()
	memoryContext, cancelMemory := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelMemory()
	memoryDone := make(chan error, 1)
	go func() {
		memoryDone <- workspacememory.Run(memoryContext, store.Dir, io.Discard)
	}()
	libraryDone := make(chan error, 1)
	go func() {
		libraryDone <- qlibrary.Run(runtimeContext, store.Dir, io.Discard)
	}()
	workspaceStore, err := workspace.DefaultStore()
	if err != nil {
		return err
	}
	if err := workspaceStore.MigrateLegacySession(); err != nil {
		return err
	}
	sessions, err := workspaceStore.ListSessions()
	if err != nil {
		return err
	}
	loaded, err := store.Load()
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return err
	}

	manager, managerErr := providerhost.NewManager(providerContext, providerhost.Store{Dir: store.Dir})
	if managerErr != nil {
		return managerErr
	}
	defer manager.Close()

	factory := managedClientFactory(manager)
	lifecycle := newStartupLifecycle()
	initialModel := newManagedModel(runtimeContext, store, factory, manager)
	initialModel.workspaceStore = &workspaceStore
	initialModel.sessions = sessions
	initialModel.sessionPickerRequired = true
	initialModel.screen = screenSessions
	initialModel.config = loaded
	if err != nil {
		initialModel.config = config.Default()
	}
	initialModel.initializing = true
	initialModel.status = "Starting Gateway and workspace services…"
	startup := startupRequest{
		ctx: runtimeContext, store: store, workspaceStore: workspaceStore,
		memoryCtx: memoryContext, loaded: loaded, configErr: err,
		manager: manager, factory: factory, lifecycle: lifecycle,
	}
	initialModel.startup = startStartup(startup.run, initialModelLoadWait)

	final, runErr := tea.NewProgram(initialModel).Run()
	if finalModel, ok := final.(model); ok && finalModel.workspaceLock != nil {
		runErr = errors.Join(runErr, finalModel.workspaceLock.Close())
	}
	cancelRuntime()
	lifecycle.waitIfStarted()
	resourcesCloseErr := lifecycle.closeResources()
	var clientCloseErr error
	if finalModel, ok := final.(model); ok && finalModel.client != nil {
		clientCloseErr = finalModel.client.Close()
	} else if startupClient := lifecycle.startupClient(); startupClient != nil {
		clientCloseErr = startupClient.Close()
	}
	cancelProvider()
	cancelMemory()
	memoryErr := <-memoryDone
	libraryErr := <-libraryDone
	return errors.Join(runErr, resourcesCloseErr, clientCloseErr, memoryErr, libraryErr)
}

func RunDefault(ctx context.Context) error {
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	if err := Run(ctx, store); err != nil {
		return fmt.Errorf("q: %w", err)
	}
	return nil
}

// RunGatewayConfig opens only the Gateway settings UI. It does not
// acquire a workspace lock or initialize chat, Session Store, Loom, or tools.
func RunGatewayConfig(ctx context.Context, store config.Store) error {
	runtimeContext, cancelRuntime := context.WithCancel(ctx)
	defer cancelRuntime()

	manager, err := providerhost.NewManager(runtimeContext, providerhost.Store{Dir: store.Dir})
	if err != nil {
		return err
	}
	defer manager.Close()

	startupErr := manager.LoadAndStart(runtimeContext)
	if errors.Is(startupErr, providerhost.ErrNotFound) {
		startupErr = nil
	}
	m := newManagedModel(runtimeContext, store, managedClientFactory(manager), manager)
	m.gatewayConfigOnly = true
	m.config = config.Default()
	m.enterGatewaySettings()
	if startupErr != nil {
		m.status = startupErr.Error()
	}

	_, runErr := runStandalone(m, screenGateway)
	cancelRuntime()
	return runErr
}

func RunGatewayConfigDefault(ctx context.Context) error {
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	if err := RunGatewayConfig(ctx, store); err != nil {
		return fmt.Errorf("q gateway config: %w", err)
	}
	return nil
}
