package workspacememory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path/filepath"

	"github.com/snowmerak/q/opensearchindex"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/storageconfig"
)

func openSearchStoreOpener(client *opensearchindex.Client) StoreOpener {
	return func(ctx context.Context, root string, options sessionstore.OpenOptions) (sessionstore.SearchIndex, error) {
		sum := sha256.Sum256([]byte(filepath.Clean(root)))
		logicalName := "wm-" + hex.EncodeToString(sum[:8])
		idx, _, err := opensearchindex.OpenIndex(ctx, client, logicalName, options.Vector)
		if err != nil {
			return nil, err
		}
		return idx, nil
	}
}

// ResolveStoreOpener reads <dir>/storage.json and returns an optional StoreOpener
// plus its shared client closer when remote OpenSearch workspace memory is enabled.
func ResolveStoreOpener(dir string) (StoreOpener, io.Closer, error) {
	cfg, err := (storageconfig.Store{Dir: dir}).LoadOrDefault()
	if err != nil {
		return nil, nil, err
	}
	if !cfg.UseRemoteOpenSearch(storageconfig.TargetWorkspaceMemory) {
		return nil, nil, nil
	}
	client, err := opensearchindex.NewClient(opensearchindex.Config{
		Endpoint:    cfg.Remote.OpenSearch.Endpoint,
		Username:    cfg.Remote.OpenSearch.Username,
		Password:    cfg.ResolveOpenSearchPassword(),
		BearerToken: cfg.ResolveOpenSearchBearerToken(),
		IndexPrefix: cfg.Remote.OpenSearch.IndexPrefix,
		Refresh:     cfg.Remote.OpenSearch.Refresh,
	})
	if err != nil {
		return nil, nil, err
	}
	return openSearchStoreOpener(client), client, nil
}
