// Package app implements q's interactive application and embeddable Agent Loop.
package app

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/internal/hostruntime"
	"github.com/snowmerak/q/providerhost"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/usagelog"
	"github.com/snowmerak/q/workspace"
)

// ChatClient is the application client's chat, model discovery, and lifetime
// contract. It satisfies agentloop.ChatClient for loop requests.
type ChatClient interface {
	agentloop.ChatClient
	ListModels(context.Context) ([]client.Model, error)
	Close() error
}

type chatClient = ChatClient

type clientFactory func(config.Config) (chatClient, error)

type providerRuntime interface {
	Endpoint() string
	APIKey() string
	Config() gateway.Config
	Apply(context.Context, gateway.Config) error
}

// AgentToolRuntime exposes the workspace tools and host environment consumed
// by Q's agent loop. The component that creates a runtime owns its lifetime.
type AgentToolRuntime = agentloop.ToolRuntime

type agentToolRuntime = AgentToolRuntime

type skillHintSearcher interface {
	SearchSkillHints(context.Context, string, int) (qtools.SkillHintSearchResult, error)
}

func managedClientFactory(runtime providerRuntime, recorder client.UsageRecorder) clientFactory {
	return func(value config.Config) (chatClient, error) {
		endpoint := runtime.Endpoint()
		if endpoint == "" {
			return nil, errors.New("internal LLM Gateway is not running")
		}
		apiKey := runtime.APIKey()
		if apiKey == "" {
			return nil, errors.New("internal LLM Gateway API key is unavailable")
		}
		providerModes := providerhost.PreferredProviderAPIModes(runtime.Config())
		return client.New(client.Config{
			BaseURL:              endpoint,
			APIKey:               apiKey,
			DefaultModel:         value.Provider.Model,
			ModelAPIModes:        value.EffectiveModelAPIModes(),
			ProviderAPIModes:     providerModes,
			UsageRecorder:        recorder,
			ForwardUsageMetadata: true,
		})
	}
}

func newUsageRecorder(store config.Store) *usagelog.Recorder {
	return usagelog.New(store.Dir)
}

// Run loads personal configuration and starts the interactive application.
// A missing configuration opens the first-run provider setup screen.
func Run(ctx context.Context, store config.Store) (returnErr error) {
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

	host, err := hostruntime.Open(ctx, hostruntime.Options{Directory: store.Dir})
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, host.Close()) }()

	runtimeContext := host.Context()
	memoryContext := host.MemoryContext()
	manager := host.Manager()
	factory := managedClientFactory(manager, host.Recorder())
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
	host.Cancel()
	lifecycle.waitIfStarted()
	resourcesCloseErr := lifecycle.closeResources()
	var clientCloseErr error
	if finalModel, ok := final.(model); ok && finalModel.client != nil {
		clientCloseErr = finalModel.client.Close()
	} else if startupClient := lifecycle.startupClient(); startupClient != nil {
		clientCloseErr = startupClient.Close()
	}
	return errors.Join(runErr, resourcesCloseErr, clientCloseErr)
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
func RunGatewayConfig(ctx context.Context, store config.Store) (returnErr error) {
	runtimeContext, cancelRuntime := context.WithCancel(ctx)
	defer cancelRuntime()

	manager, err := providerhost.NewManager(runtimeContext, providerhost.Store{Dir: store.Dir})
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, manager.Close()) }()

	startupErr := manager.LoadAndStart(runtimeContext)
	if errors.Is(startupErr, providerhost.ErrNotFound) {
		startupErr = nil
	}
	usageRecorder := newUsageRecorder(store)
	defer func() { returnErr = errors.Join(returnErr, usageRecorder.Close()) }()
	m := newManagedModel(runtimeContext, store, managedClientFactory(manager, usageRecorder), manager)
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
		return fmt.Errorf("q gateway: %w", err)
	}
	return nil
}
