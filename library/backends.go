package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/worklock"
)

const (
	SearchPurposeSkills    = "skills"
	SearchPurposeKnowledge = "knowledge"
)

// WorkItemsOpener opens the authoritative work-item repository (such as local
// SQLite or PostgreSQL).
type WorkItemsOpener func(ctx context.Context, dir string) (workitems.Repository, error)

// SearchIndexSpec describes one logical derived search index opened by the
// Library service.
type SearchIndexSpec struct {
	Purpose     string
	Generation  string
	Namespace   string
	WorkspaceID string
	Vector      sessionstore.VectorConfig
}

// OpenedSearchIndex pairs an opened SearchIndex with whether the index was
// freshly created and needs an outbox reset/backfill.
type OpenedSearchIndex struct {
	Index   sessionstore.SearchIndex
	Created bool
}

// SearchBackend opens derived search indexes for global skills and workspace
// knowledge (for example, local Bleve+HNSW or OpenSearch).
type SearchBackend interface {
	// MappingVersion identifies the backend mapping schema used in projection
	// generation digests. Returning "" uses sessionstore.MappingVersion.
	MappingVersion() string
	Open(ctx context.Context, spec SearchIndexSpec) (OpenedSearchIndex, error)
}

// Backends configures custom storage and search implementations for Library.
// SkillsSearch and KnowledgeSearch override Search for their respective index
// purposes. Shared client handles in Closers are closed once after knowledge,
// archive, and repository shutdown completes.
type Backends struct {
	OpenWorkItems   WorkItemsOpener
	Search          SearchBackend
	SkillsSearch    SearchBackend
	KnowledgeSearch SearchBackend
	Closers         []io.Closer
}

func (b Backends) skillsBackend(fallback SearchBackend) SearchBackend {
	if b.SkillsSearch != nil {
		return b.SkillsSearch
	}
	if b.Search != nil {
		return b.Search
	}
	return fallback
}

func (b Backends) knowledgeBackend(fallback SearchBackend) SearchBackend {
	if b.KnowledgeSearch != nil {
		return b.KnowledgeSearch
	}
	if b.Search != nil {
		return b.Search
	}
	return fallback
}

type localSearchBackend struct {
	dir  string
	lock *worklock.Lock
}

func (b localSearchBackend) MappingVersion() string { return "" }

func (b localSearchBackend) Open(ctx context.Context, spec SearchIndexSpec) (OpenedSearchIndex, error) {
	switch spec.Purpose {
	case SearchPurposeSkills:
		index, err := sessionstore.OpenLocalIndex(ctx, b.dir, sessionstore.OpenOptions{
			WorkspaceLock: b.lock,
			Directory:     "library",
			Vector:        spec.Vector,
		})
		if err != nil {
			return OpenedSearchIndex{}, err
		}
		return OpenedSearchIndex{Index: index}, nil
	case SearchPurposeKnowledge:
		directory := filepath.Join("library", "knowledge-index", spec.Generation, spec.Namespace)
		_, statErr := os.Stat(filepath.Join(b.dir, directory, "data", "records"))
		index, err := sessionstore.OpenLocalIndex(ctx, b.dir, sessionstore.OpenOptions{
			Directory:     directory,
			WorkspaceLock: b.lock,
			Vector:        spec.Vector,
		})
		if err != nil {
			return OpenedSearchIndex{}, err
		}
		return OpenedSearchIndex{Index: index, Created: errors.Is(statErr, os.ErrNotExist)}, nil
	default:
		return OpenedSearchIndex{}, fmt.Errorf("library: unsupported search index purpose %q", spec.Purpose)
	}
}
