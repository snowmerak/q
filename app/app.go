// Package app implements q's interactive Bubble Tea application.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/sessionstore"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
)

type chatClient interface {
	Chat(context.Context, client.ChatRequest) (*client.ChatResponse, error)
	ListModels(context.Context) ([]client.Model, error)
	Close() error
}

type clientFactory func(config.Config) (chatClient, error)

type providerRuntime interface {
	Endpoint() string
	Config() gateway.Config
	Apply(context.Context, gateway.Config) error
}

type agentToolRuntime interface {
	Tools() []client.Tool
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
		return client.New(client.Config{
			BaseURL:       endpoint,
			DefaultModel:  value.Provider.Model,
			DisableAPIKey: true,
		})
	}
}

// Run loads personal configuration and starts the interactive application.
// A missing configuration opens the first-run provider setup screen.
func Run(ctx context.Context, store config.Store) error {
	workspaceStore, err := workspace.DefaultStore()
	if err != nil {
		return err
	}
	loaded, err := store.Load()
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return err
	}

	manager, managerErr := providerhost.NewManager(ctx, providerhost.Store{Dir: store.Dir})
	if managerErr != nil {
		return managerErr
	}
	defer manager.Close()

	startupErr := manager.LoadAndStart(ctx)
	if errors.Is(startupErr, providerhost.ErrNotFound) && err == nil && !loaded.Provider.Managed {
		legacy, legacyErr := providerhost.LegacyProvider(
			"default", loaded.Provider.BaseURL, loaded.Provider.APIKeyEnv, loaded.Provider.APIKey,
		)
		if legacyErr == nil {
			startupErr = manager.Apply(ctx, gateway.Config{Providers: []gateway.ProviderConfig{legacy}})
			if startupErr == nil {
				if !strings.HasPrefix(loaded.Provider.Model, "default/") {
					loaded.Provider.Model = "default/" + loaded.Provider.Model
				}
				loaded.UseManagedGateway()
				startupErr = store.Save(loaded)
			}
		} else {
			startupErr = legacyErr
		}
	} else if errors.Is(startupErr, providerhost.ErrNotFound) {
		startupErr = nil
	}

	factory := managedClientFactory(manager)
	archiveStore, archiveOpenErr := sessionstore.Open(workspaceStore.Root)
	var agentTools *qtools.Runtime
	var toolsErr error
	if archiveOpenErr == nil {
		agentTools, toolsErr = qtools.NewRuntimeWithArchive(ctx, workspaceStore.Root, archiveStore)
	} else {
		agentTools, toolsErr = qtools.NewRuntime(ctx, workspaceStore.Root)
	}
	if toolsErr != nil {
		if archiveStore != nil {
			_ = archiveStore.Close()
		}
		return toolsErr
	}
	defer agentTools.Close()
	var archiveWriter *sessionstore.Writer
	if archiveOpenErr == nil {
		archiveWriter = sessionstore.NewWriter(archiveStore, 0)
	}
	initialModel := newManagedModel(ctx, store, factory, manager)
	initialModel.workspaceStore = &workspaceStore
	initialModel.toolRuntime = agentTools
	initialModel.archive = archiveWriter
	if err == nil && manager.Endpoint() != "" {
		if !loaded.Provider.Managed {
			loaded.UseManagedGateway()
		}
		configuredClient, clientErr := factory(loaded)
		if clientErr != nil {
			if archiveWriter != nil {
				_ = archiveWriter.Close()
			}
			return clientErr
		}
		if loaded.EffectiveContextWindow() == 0 {
			if models, modelsErr := configuredClient.ListModels(ctx); modelsErr == nil {
				for _, candidate := range models {
					if candidate.ID == loaded.Provider.Model && candidate.ContextLength > 0 {
						loaded.Provider.ContextWindow = candidate.ContextLength
						_ = store.Save(loaded)
						break
					}
				}
			}
		}
		initialModel.enterChat(loaded, configuredClient)
	} else if len(manager.Config().Providers) > 0 {
		initialModel.enterProviderList()
	}
	if startupErr != nil {
		initialModel.status = startupErr.Error()
	}
	if archiveOpenErr != nil {
		if initialModel.status != "" {
			initialModel.status += " · "
		}
		initialModel.status += "archive: " + archiveOpenErr.Error()
	}

	final, runErr := tea.NewProgram(initialModel).Run()
	if finalModel, ok := final.(model); ok && finalModel.client != nil {
		_ = finalModel.client.Close()
	} else if initialModel.client != nil {
		_ = initialModel.client.Close()
	}
	var archiveCloseErr error
	if archiveWriter != nil {
		archiveCloseErr = archiveWriter.Close()
	}
	return errors.Join(runErr, archiveCloseErr)
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
