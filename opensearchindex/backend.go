package opensearchindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workspacememory"
)

const MappingVersion = "opensearch-v1"

// LibraryBackend plugs a shared *Client into library.Backends for skills
// and knowledge projections.
type LibraryBackend struct {
	client *Client
}

var _ library.SearchBackend = (*LibraryBackend)(nil)

// NewLibraryBackend returns a library.SearchBackend backed by client.
func NewLibraryBackend(client *Client) *LibraryBackend {
	return &LibraryBackend{client: client}
}

func (b *LibraryBackend) MappingVersion() string { return MappingVersion }

func (b *LibraryBackend) Open(ctx context.Context, spec library.SearchIndexSpec) (library.OpenedSearchIndex, error) {
	if b == nil || b.client == nil {
		return library.OpenedSearchIndex{}, fmt.Errorf("opensearchindex: library backend requires a non-nil client")
	}
	var logicalName string
	switch spec.Purpose {
	case library.SearchPurposeSkills:
		ns := spec.Namespace
		if ns == "" {
			ns = "global"
		}
		logicalName = "skills-" + ns
	case library.SearchPurposeKnowledge:
		ns := spec.Namespace
		if ns == "" {
			ns = "all"
		}
		logicalName = "knowledge-" + spec.Generation + "-" + ns
	default:
		return library.OpenedSearchIndex{}, fmt.Errorf("opensearchindex: unsupported library search purpose %q", spec.Purpose)
	}
	idx, created, err := OpenIndex(ctx, b.client, logicalName, spec.Vector)
	if err != nil {
		return library.OpenedSearchIndex{}, err
	}
	return library.OpenedSearchIndex{Index: idx, Created: created}, nil
}

// WorkspaceMemoryOpener returns a workspacememory.StoreOpener backed by client.
// The shared client is NOT closed when individual workspaces close.
func WorkspaceMemoryOpener(client *Client) workspacememory.StoreOpener {
	return func(ctx context.Context, root string, options sessionstore.OpenOptions) (sessionstore.SearchIndex, error) {
		sum := sha256.Sum256([]byte(filepath.Clean(root)))
		logicalName := "wm-" + hex.EncodeToString(sum[:8])
		idx, _, err := OpenIndex(ctx, client, logicalName, options.Vector)
		if err != nil {
			return nil, err
		}
		return idx, nil
	}
}
