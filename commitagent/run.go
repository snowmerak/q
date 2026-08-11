package commitagent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
)

type resolvedRuntime struct {
	client *client.Client
	spec   subagent.Spec
	close  func() error
}

type runtimeResult struct {
	runtime resolvedRuntime
	err     error
}

type repositoryResult struct {
	state repositoryState
	err   error
}

func prepareSessionDefault(ctx context.Context, directory string, logger *progressLogger) (*Session, error) {
	store, err := config.DefaultStore()
	if err != nil {
		return nil, err
	}
	value, err := store.Load()
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			return nil, errors.New("q commit: q is not configured; run q first")
		}
		return nil, err
	}

	workContext, cancel := context.WithCancel(ctx)
	repositoryChannel := make(chan repositoryResult, 1)
	runtimeChannel := make(chan runtimeResult, 1)
	go func() {
		logger.step("prepare", "inspecting the Git index")
		state, prepareErr := prepareRepository(workContext, directory)
		if prepareErr == nil {
			if state.autoStaged {
				logger.step("prepare", "staged all working-tree changes")
			}
			logger.step("prepare", "collected %d visible and %d lock files", len(state.visibleFiles), len(state.lockFiles))
		}
		repositoryChannel <- repositoryResult{state: state, err: prepareErr}
	}()
	go func() {
		logger.step("model", "resolving the commit agent model")
		runtime, resolveErr := resolveCommitRuntime(workContext, store, value)
		if resolveErr == nil {
			effort := runtime.spec.ReasoningEffort
			if effort == "" {
				effort = "provider default"
			}
			logger.step("model", "resolved %s (%s)", runtime.spec.Model, effort)
		} else {
			logger.step("model", "model resolution failed")
		}
		runtimeChannel <- runtimeResult{runtime: runtime, err: resolveErr}
	}()

	repository := <-repositoryChannel
	if repository.err != nil {
		cancel()
		resolved := <-runtimeChannel
		if resolved.err == nil {
			_ = resolved.runtime.close()
		}
		return nil, repository.err
	}
	logger.step("detect", "checking for trivial staged changes")
	trivial, err := detectTrivialChange(ctx, repository.state)
	if err != nil {
		cancel()
		resolved := <-runtimeChannel
		if resolved.err == nil {
			_ = resolved.runtime.close()
		}
		return nil, err
	}
	resolved := <-runtimeChannel
	loomOptions := value.LoomStoreOptions(func(ctx context.Context) ([]loom.Ref, error) {
		return workspace.LoomReferencesAt(ctx, repository.state.root)
	})
	session := &Session{
		state: repository.state, maxParallel: value.EffectiveAgents().MaxParallel,
		runtime: resolved.runtime, runtimeErr: resolved.err, loomOptions: loomOptions,
		logger: logger, cancel: cancel,
	}
	if trivial.Found {
		logger.step("detect", "matched %s", trivial.Message)
		proposal := proposalForTrivialMessage(trivial.Message)
		session.proposal = proposalState{Single: &proposal}
		session.source = "trivial detector"
		return session, nil
	}
	if len(repository.state.visibleFiles) == 0 {
		logger.step("fallback", "only lock files changed")
		proposal := Proposal{Type: "chore", Scope: "deps", Summary: "updated dependency locks"}
		session.proposal = proposalState{Single: &proposal}
		session.source = "mechanical fallback"
		return session, nil
	}
	if resolved.err != nil {
		cancel()
		return nil, resolved.err
	}
	if err := session.Regenerate(ctx, logger); err != nil {
		_ = session.Close()
		return nil, err
	}
	return session, nil
}

func resolveCommitRuntime(ctx context.Context, store config.Store, value config.Config) (resolvedRuntime, error) {
	var configuredClient *client.Client
	var closeRuntime func() error
	if value.Provider.Managed {
		manager, err := providerhost.NewManager(ctx, providerhost.Store{Dir: store.Dir})
		if err != nil {
			return resolvedRuntime{}, err
		}
		if err := manager.LoadAndStart(ctx); err != nil {
			_ = manager.Close()
			return resolvedRuntime{}, fmt.Errorf("q commit: start provider gateway: %w", err)
		}
		configuredClient, err = client.New(client.Config{
			BaseURL: manager.Endpoint(), APIKey: manager.APIKey(), DefaultModel: value.Provider.Model,
		})
		if err != nil {
			_ = manager.Close()
			return resolvedRuntime{}, err
		}
		closeRuntime = func() error {
			return errors.Join(configuredClient.Close(), manager.Close())
		}
	} else {
		apiKey := value.Provider.ResolveAPIKey()
		var err error
		configuredClient, err = client.New(client.Config{
			BaseURL: value.Provider.BaseURL, APIKey: apiKey, DefaultModel: value.Provider.Model, DisableAPIKey: apiKey == "",
		})
		if err != nil {
			return resolvedRuntime{}, err
		}
		closeRuntime = configuredClient.Close
	}

	models, err := configuredClient.ListModels(ctx)
	if err != nil {
		_ = closeRuntime()
		return resolvedRuntime{}, fmt.Errorf("q commit: load models: %w", err)
	}
	spec, err := subagent.Resolve(value, config.AgentRoleCommit, models)
	if err != nil {
		_ = closeRuntime()
		return resolvedRuntime{}, err
	}
	return resolvedRuntime{client: configuredClient, spec: spec, close: closeRuntime}, nil
}

func proposalForTrivialMessage(message string) Proposal {
	summary := strings.TrimPrefix(message, "style: ")
	return Proposal{Type: "style", Summary: summary}
}
