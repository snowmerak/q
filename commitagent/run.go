package commitagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/subagent"
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

// RunDefault loads q's personal provider and commit-agent settings, prepares
// staged changes, and creates one or more commits.
func RunDefault(ctx context.Context, directory string, output io.Writer) (Result, error) {
	store, err := config.DefaultStore()
	if err != nil {
		return Result{}, err
	}
	value, err := store.Load()
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			return Result{}, errors.New("q commit: q is not configured; run q first")
		}
		return Result{}, err
	}

	workContext, cancel := context.WithCancel(ctx)
	defer cancel()
	repositoryChannel := make(chan repositoryResult, 1)
	runtimeChannel := make(chan runtimeResult, 1)
	go func() {
		state, prepareErr := prepareRepository(workContext, directory)
		repositoryChannel <- repositoryResult{state: state, err: prepareErr}
	}()
	go func() {
		runtime, resolveErr := resolveCommitRuntime(workContext, store, value)
		runtimeChannel <- runtimeResult{runtime: runtime, err: resolveErr}
	}()

	repository := <-repositoryChannel
	if repository.err != nil {
		cancel()
		resolved := <-runtimeChannel
		if resolved.err == nil {
			_ = resolved.runtime.close()
		}
		return Result{}, repository.err
	}
	trivial, err := detectTrivialChange(ctx, repository.state)
	if err != nil {
		cancel()
		resolved := <-runtimeChannel
		if resolved.err == nil {
			_ = resolved.runtime.close()
		}
		return Result{}, err
	}
	if trivial.Found {
		cancel()
		resolved := <-runtimeChannel
		if resolved.err == nil {
			_ = resolved.runtime.close()
		}
		proposal := proposalForTrivialMessage(trivial.Message)
		return executeProposal(ctx, repository.state, proposalState{Single: &proposal}, "trivial detector", output)
	}
	if len(repository.state.visibleFiles) == 0 {
		cancel()
		resolved := <-runtimeChannel
		if resolved.err == nil {
			_ = resolved.runtime.close()
		}
		proposal := Proposal{Type: "chore", Scope: "deps", Summary: "updated dependency locks"}
		return executeProposal(ctx, repository.state, proposalState{Single: &proposal}, "mechanical fallback", output)
	}

	resolved := <-runtimeChannel
	if resolved.err != nil {
		return Result{}, resolved.err
	}
	defer resolved.runtime.close()
	proposal, fallback, err := runCommitAgent(
		ctx, resolved.runtime.client, resolved.runtime.spec, repository.state, value.EffectiveAgents().MaxParallel,
	)
	if err != nil {
		return Result{}, err
	}
	source := "commit agent"
	if fallback {
		source = "mechanical fallback"
	}
	return executeProposal(ctx, repository.state, proposal, source, output)
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
			BaseURL: manager.Endpoint(), DefaultModel: value.Provider.Model, DisableAPIKey: true,
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
