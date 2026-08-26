// Package tools constructs q's MCP tool server.
package tools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/archiveembed"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/lsp"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/tools/builtin"
	"github.com/snowmerak/q/workspace"
	"github.com/snowmerak/q/workspacememory"
)

const (
	ServerName    = "q-tools"
	ServerVersion = "0.1.0"
)

// NewServer builds an MCP server with the root-jailed builtin tool set.
func NewServer(root string) (*mcp.Server, error) {
	loomRuntime, err := newLoomRuntime(root, loom.NewProcessEvaluator(), withSessionRoots(loom.StoreOptions{}, root))
	if err != nil {
		return nil, err
	}
	server, _, _, err := newServer(root, nil, loomRuntime, nil, nil)
	return server, err
}

// NewServerWithArchive builds an MCP server whose read-only archive tools use
// the supplied workspace store. The caller owns the archive lifetime.
func NewServerWithArchive(root string, archive builtin.Archive) (*mcp.Server, error) {
	loomRuntime, err := newLoomRuntime(root, loom.NewProcessEvaluator(), withSessionRoots(loom.StoreOptions{}, root))
	if err != nil {
		return nil, err
	}
	server, _, _, err := newServer(root, archive, loomRuntime, nil, nil)
	return server, err
}

func newServer(root string, archive builtin.Archive, loomRuntime *builtin.LoomRuntime, lspManager *lsp.Manager, globalSkills builtin.GlobalSkillLibrary) (*mcp.Server, *builtin.FS, *agentskills.Registry, error) {
	skills, err := agentskills.Discover(root)
	if err != nil {
		return nil, nil, nil, err
	}
	if store, ok := archive.(agentskills.RecordStore); ok {
		var syncErr error
		if globalSkills == nil {
			syncErr = skills.SyncRecords(context.Background(), store)
		} else {
			syncErr = skills.SyncRecordsForScopes(context.Background(), store, "project")
		}
		if syncErr != nil {
			return nil, nil, nil, fmt.Errorf("tools: index Agent Skills: %w", syncErr)
		}
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    ServerName,
		Version: ServerVersion,
	}, nil)
	propositions, _ := globalSkills.(builtin.PropositionLibrary)
	fs, err := builtin.Register(server, root, builtin.Dependencies{
		Archive: archive, Loom: loomRuntime, Skills: skills, GlobalSkills: globalSkills,
		Propositions: propositions, LSP: lspManager,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return server, fs, skills, nil
}

func newLoomRuntime(root string, evaluator loom.Evaluator, options loom.StoreOptions) (*builtin.LoomRuntime, error) {
	store, err := loom.OpenWithOptions(root, options)
	if err != nil {
		return nil, err
	}
	return &builtin.LoomRuntime{Store: store, Evaluator: evaluator}, nil
}

// RunStdio serves the builtin tools over MCP stdio until ctx is cancelled or
// the client disconnects. Callers must keep stdout reserved for MCP messages.
func RunStdio(ctx context.Context, root string) error {
	return RunStdioWithLoomOptions(ctx, root, loom.StoreOptions{})
}

func RunStdioWithLoomOptions(ctx context.Context, root string, options loom.StoreOptions) (runErr error) {
	workspaceLock, err := workspace.AcquireLock(root, "q-mcp")
	if err != nil {
		return err
	}
	defer workspaceLock.Close()

	globalLSP := lsp.GlobalConfig{}
	configStore, err := config.DefaultStore()
	if err != nil {
		return err
	}
	memoryContext, cancelMemory := context.WithCancel(context.WithoutCancel(ctx))
	memoryDone := make(chan error, 1)
	go func() {
		memoryDone <- workspacememory.Run(memoryContext, configStore.Dir, nil)
	}()
	defer func() {
		cancelMemory()
		runErr = errors.Join(runErr, <-memoryDone)
	}()
	loaded, err := configStore.Load()
	if err == nil {
		globalLSP = loaded.LSP
	} else if !errors.Is(err, config.ErrNotFound) {
		return err
	}
	vectorConfig := sessionstore.VectorConfig{}
	if loaded.Embedding.Model != "" {
		vectorConfig = sessionstore.VectorConfig{
			Model: loaded.Embedding.Model, Dimensions: loaded.Embedding.Dimensions,
		}
	}
	memoryRuntime, err := workspacememory.Ensure(memoryContext, configStore.Dir)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, memoryRuntime.Close()) }()
	archive, err := memoryRuntime.Client().OpenWorkspace(ctx, root, vectorConfig)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, archive.Close()) }()
	semanticArchive := archiveembed.New(archive)
	loomRuntime, err := newLoomRuntime(root, loom.NewProcessEvaluator(), withSessionRoots(options, root))
	if err != nil {
		return err
	}
	workspaceLSP, err := (workspace.Store{Root: root}).LoadLSP()
	if err != nil {
		return err
	}
	lspManager, err := lsp.NewManager(ctx, root, globalLSP, workspaceLSP)
	if err != nil {
		return err
	}
	defer lspManager.Close()
	libraryConfig, libraryConfigErr := (qlibrary.ConfigStore{Dir: configStore.Dir}).LoadOrDefault()
	if libraryConfigErr != nil {
		libraryConfig = qlibrary.DefaultConfig()
	}
	libraryClient := qlibrary.NewClient(
		libraryConfig.Endpoint(), libraryConfig.ResolveAPIKey(), 5*time.Second,
	)
	var embeddingClient *client.Client
	var embeddingManager *providerhost.Manager
	if loaded.Embedding.Model != "" {
		if loaded.Provider.Managed {
			embeddingManager, err = providerhost.NewManager(ctx, providerhost.Store{Dir: configStore.Dir})
			if err != nil {
				return err
			}
			if err = embeddingManager.LoadAndStart(ctx); err != nil {
				_ = embeddingManager.Close()
				return err
			}
			embeddingClient, err = client.New(client.Config{
				BaseURL: embeddingManager.Endpoint(), APIKey: embeddingManager.APIKey(),
				DefaultModel: loaded.Provider.Model,
			})
		} else {
			apiKey := loaded.Provider.ResolveAPIKey()
			embeddingClient, err = client.New(client.Config{
				BaseURL: loaded.Provider.BaseURL, APIKey: apiKey, DefaultModel: loaded.Provider.Model,
				DisableAPIKey: apiKey == "",
			})
		}
		if err != nil {
			if embeddingManager != nil {
				_ = embeddingManager.Close()
			}
			return err
		}
		if err = libraryClient.ConfigureEmbedding(
			embeddingClient, loaded.Embedding.Model, loaded.Embedding.Dimensions,
		); err != nil {
			_ = embeddingClient.Close()
			if embeddingManager != nil {
				_ = embeddingManager.Close()
			}
			return err
		}
		if err = semanticArchive.Configure(
			embeddingClient, loaded.Embedding.Model, loaded.Embedding.Dimensions,
		); err != nil {
			_ = embeddingClient.Close()
			if embeddingManager != nil {
				_ = embeddingManager.Close()
			}
			return err
		}
		go func() { _, _ = semanticArchive.Backfill(ctx) }()
		defer embeddingClient.Close()
		if embeddingManager != nil {
			defer embeddingManager.Close()
		}
	}
	var globalSkills builtin.GlobalSkillLibrary = libraryClient
	var libraryRuntime *qlibrary.Runtime
	libraryRuntime, err = qlibrary.Ensure(ctx, configStore.Dir)
	if err != nil {
		libraryRuntime = nil
	}
	if libraryRuntime != nil {
		defer libraryRuntime.Close()
	}
	server, fs, _, err := newServer(root, semanticArchive, loomRuntime, lspManager, globalSkills)
	if err != nil {
		return err
	}
	defer fs.Close()
	return server.Run(ctx, &mcp.StdioTransport{})
}

func withSessionRoots(options loom.StoreOptions, root string) loom.StoreOptions {
	options.Roots = func(ctx context.Context) ([]loom.Ref, error) {
		return workspace.LoomReferencesAt(ctx, root)
	}
	return options
}
