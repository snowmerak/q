package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/snowmerak/q/archiveembed"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workspace"
	"github.com/snowmerak/q/workspacememory"
)

const embeddingRequestTimeout = 5 * time.Minute

// SyncEmbeddingArchive repairs one existing archive using freshly loaded settings.
// An empty root selects the global Library. Unlike SyncEmbeddings, this operation
// does not migrate the workspace catalog or create a missing workspace archive.
func (host *SessionHost) SyncEmbeddingArchive(ctx context.Context, root string) (embedded int, returnErr error) {
	if host == nil || host.runtime == nil {
		return 0, fmt.Errorf("%w: embedding runtime is unavailable", ErrSessionRuntimeUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	value, err := host.store.Load()
	if err != nil {
		return 0, err
	}
	if value.Embedding.Model == "" || value.Embedding.Dimensions <= 0 {
		return 0, errors.New("embeddings are disabled")
	}
	if root != "" {
		info, err := os.Stat(filepath.Join(root, ".q", "data", "records"))
		if err != nil {
			return 0, err
		}
		if !info.IsDir() {
			return 0, errors.New("workspace archive records path is not a directory")
		}
		if err := host.releaseIdleWorkspaceSessions(root); err != nil {
			return 0, err
		}
	}
	value, err = host.ensureProviderContext(ctx, value)
	if err != nil {
		return 0, err
	}
	configuredClient, err := host.factory(value)
	if err != nil {
		return 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, configuredClient.Close()) }()
	embedder, ok := configuredClient.(qlibrary.Embedder)
	if !ok {
		return 0, errors.New("configured LLM client does not support embeddings")
	}
	// Reject provider results when saved settings changed during generation.
	guard := &currentEmbeddingProvider{store: host.store, model: value.Embedding.Model, dimensions: value.Embedding.Dimensions, provider: embedder}
	embedder = guard
	if err := guard.check(ctx); err != nil {
		return 0, err
	}
	if root == "" {
		libraryRuntime, err := qlibrary.Ensure(ctx, host.store.Dir)
		if err != nil {
			return 0, err
		}
		defer func() { returnErr = errors.Join(returnErr, libraryRuntime.Close()) }()
		if err := libraryRuntime.Client().ConfigureEmbedding(embedder, value.Embedding.Model, value.Embedding.Dimensions); err != nil {
			return 0, err
		}
		stats, err := libraryRuntime.Client().SyncEmbeddings(ctx)
		return stats.Skills + stats.Propositions, errors.Join(err, guard.check(ctx))
	}
	memoryRuntime, err := workspacememory.Ensure(ctx, host.store.Dir)
	if err != nil {
		return 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, memoryRuntime.Close()) }()
	memory := workspacememory.NewClient(memoryRuntime.Endpoint(), "", embeddingRequestTimeout)
	archiveStore, err := memory.OpenWorkspace(ctx, root, sessionstore.VectorConfig{Model: value.Embedding.Model, Dimensions: value.Embedding.Dimensions})
	if err != nil {
		return 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, archiveStore.Close()) }()
	archive := archiveembed.New(archiveStore)
	if err := archive.ConfigureContext(ctx, embedder, value.Embedding.Model, value.Embedding.Dimensions); err != nil {
		return 0, err
	}
	stats, err := archive.Backfill(ctx)
	return stats.Embedded, errors.Join(err, guard.check(ctx))
}

func (host *SessionHost) releaseIdleWorkspaceSessions(root string) error {
	host.liveMu.Lock()
	var sessions []workspace.Store
	for _, session := range host.liveSessions {
		current := session.prepared.store.Root
		if current == root || runtime.GOOS == "windows" && strings.EqualFold(current, root) {
			sessions = append(sessions, session.prepared.store)
		}
	}
	host.liveMu.Unlock()
	for _, store := range sessions {
		if err := host.ReleaseSession(store, store.SessionID); err != nil {
			return err
		}
	}
	return nil
}

// SetEmbeddingWorkspaceResolver supplies the owning application's persisted
// workspace catalog without coupling the host to Studio's storage format.
func (host *SessionHost) SetEmbeddingWorkspaceResolver(resolver func() ([]string, error)) {
	if host == nil {
		return
	}
	host.workspaceMu.Lock()
	host.embeddingWorkspaces = resolver
	host.workspaceMu.Unlock()
}

func (host *SessionHost) embeddingWorkspaceRoots(selected string) ([]string, error) {
	host.workspaceMu.RLock()
	resolver := host.embeddingWorkspaces
	host.workspaceMu.RUnlock()
	var candidates []string
	if resolver != nil {
		var err error
		candidates, err = resolver()
		if err != nil {
			return nil, err
		}
	}
	if selected = strings.TrimSpace(selected); selected != "" {
		absolute, err := filepath.Abs(selected)
		if err != nil {
			return nil, err
		}
		selected = absolute
		candidates = append(candidates, selected)
	}
	roots := make([]string, 0, len(candidates))
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		root, err := filepath.Abs(candidate)
		if err != nil {
			return nil, err
		}
		if err := workspace.RejectHomeDirectory(root); err != nil {
			return nil, err
		}
		// Catalog roots without archive data need no migration. Opening those
		// roots later creates an archive directly with the saved model.
		if root != selected {
			if _, err := os.Stat(filepath.Join(root, ".q", "data", "records")); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return nil, fmt.Errorf("inspect workspace archive %q: %w", root, err)
			}
		}
		key := root
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if !seen[key] {
			seen[key] = true
			roots = append(roots, root)
		}
	}
	return roots, nil
}

func syncWorkspaceEmbeddings(ctx context.Context, memory *workspacememory.Client, root string, value config.Config, embedder qlibrary.Embedder) (returnErr error) {
	vector := sessionstore.VectorConfig{}
	if value.Embedding.Model != "" {
		vector = sessionstore.VectorConfig{Model: value.Embedding.Model, Dimensions: value.Embedding.Dimensions}
	}
	archiveStore, err := memory.OpenWorkspace(ctx, root, vector)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, archiveStore.Close()) }()
	archive := archiveembed.New(archiveStore)
	if value.Embedding.Model == "" {
		return archive.Disable()
	}
	if err := archive.Configure(embedder, value.Embedding.Model, value.Embedding.Dimensions); err != nil {
		return err
	}
	_, err = archive.Backfill(ctx)
	return err
}
