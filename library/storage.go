package library

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/snowmerak/q/opensearchindex"
	"github.com/snowmerak/q/storageconfig"
	"github.com/snowmerak/q/workitems"
)

type openSearchBackend struct {
	client *opensearchindex.Client
}

var _ SearchBackend = (*openSearchBackend)(nil)

func (b *openSearchBackend) MappingVersion() string { return opensearchindex.MappingVersion }

func (b *openSearchBackend) Open(ctx context.Context, spec SearchIndexSpec) (OpenedSearchIndex, error) {
	if b == nil || b.client == nil {
		return OpenedSearchIndex{}, errors.New("library: opensearch backend requires a non-nil client")
	}
	var logicalName string
	switch spec.Purpose {
	case SearchPurposeSkills:
		ns := spec.Namespace
		if ns == "" {
			ns = "global"
		}
		logicalName = "skills-" + ns
	case SearchPurposeKnowledge:
		ns := spec.Namespace
		if ns == "" {
			ns = "all"
		}
		logicalName = "knowledge-" + spec.Generation + "-" + ns
	default:
		return OpenedSearchIndex{}, fmt.Errorf("library: unsupported search purpose %q", spec.Purpose)
	}
	idx, created, err := opensearchindex.OpenIndex(ctx, b.client, logicalName, spec.Vector)
	if err != nil {
		return OpenedSearchIndex{}, err
	}
	return OpenedSearchIndex{Index: idx, Created: created}, nil
}

// ResolveBackends reads <dir>/storage.json and builds the configured work-items
// and search backends. When local mode (or default) is selected, it returns an
// empty Backends struct so callers use the built-in SQLite + Bleve/HNSW stack.
func ResolveBackends(dir string) (Backends, error) {
	cfg, err := (storageconfig.Store{Dir: dir}).LoadOrDefault()
	if err != nil {
		return Backends{}, err
	}
	return backendsFromStorageConfig(cfg)
}

func backendsFromStorageConfig(cfg storageconfig.Config) (Backends, error) {
	cfg = cfg.Effective()
	if cfg.Mode != storageconfig.ModeRemote {
		return Backends{}, nil
	}
	var backends Backends
	if cfg.UseRemotePostgres() {
		dsn := cfg.ResolvePostgresDSN()
		if dsn == "" {
			return Backends{}, errors.New("library: remote PostgreSQL is enabled without a DSN")
		}
		backends.OpenWorkItems = func(ctx context.Context, _ string) (workitems.Repository, error) {
			return workitems.OpenPostgres(ctx, dsn)
		}
	}
	useSkills := cfg.UseRemoteOpenSearch(storageconfig.TargetSkills)
	useKnowledge := cfg.UseRemoteOpenSearch(storageconfig.TargetKnowledge)
	if useSkills || useKnowledge {
		client, err := opensearchindex.NewClient(opensearchindex.Config{
			Endpoint:    cfg.Remote.OpenSearch.Endpoint,
			Username:    cfg.Remote.OpenSearch.Username,
			Password:    cfg.ResolveOpenSearchPassword(),
			BearerToken: cfg.ResolveOpenSearchBearerToken(),
			IndexPrefix: cfg.Remote.OpenSearch.IndexPrefix,
			Refresh:     cfg.Remote.OpenSearch.Refresh,
		})
		if err != nil {
			return Backends{}, err
		}
		osBackend := &openSearchBackend{client: client}
		backends.Closers = append(backends.Closers, io.Closer(client))
		if useSkills && useKnowledge {
			backends.Search = osBackend
		} else {
			if useSkills {
				backends.SkillsSearch = osBackend
				backends.KnowledgeSearch = localSearchBackend{}
			} else {
				backends.SkillsSearch = localSearchBackend{}
				backends.KnowledgeSearch = osBackend
			}
		}
	}
	return backends, nil
}
