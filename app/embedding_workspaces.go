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
