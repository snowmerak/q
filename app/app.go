// Package app implements q's interactive Bubble Tea application.
package app

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

type chatClient interface {
	Chat(context.Context, client.ChatRequest) (*client.ChatResponse, error)
	ListModels(context.Context) ([]client.Model, error)
	Close() error
}

type clientFactory func(config.Config) (chatClient, error)

func defaultClientFactory(value config.Config) (chatClient, error) {
	apiKey := value.Provider.ResolveAPIKey()
	return client.New(client.Config{
		BaseURL:       value.Provider.BaseURL,
		APIKey:        apiKey,
		DefaultModel:  value.Provider.Model,
		DisableAPIKey: apiKey == "",
	})
}

// Run loads personal configuration and starts the interactive application.
// A missing configuration opens the first-run provider setup screen.
func Run(ctx context.Context, store config.Store) error {
	loaded, err := store.Load()
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return err
	}

	initialModel := newModel(ctx, store, defaultClientFactory)
	if err == nil {
		configuredClient, clientErr := defaultClientFactory(loaded)
		if clientErr != nil {
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
	}

	final, runErr := tea.NewProgram(initialModel).Run()
	if finalModel, ok := final.(model); ok && finalModel.client != nil {
		_ = finalModel.client.Close()
	} else if initialModel.client != nil {
		_ = initialModel.client.Close()
	}
	return runErr
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
