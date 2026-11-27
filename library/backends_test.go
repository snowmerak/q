package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workitems"
)

type spySharedClientCloser struct {
	closes atomic.Int32
}

func (c *spySharedClientCloser) Close() error {
	c.closes.Add(1)
	return nil
}

func TestEnsureWithComposedBackendsAndSplitSearch(t *testing.T) {
	dir := t.TempDir()
	workItemsDir := t.TempDir()
	skillsDir := t.TempDir()
	knowledgeDir := t.TempDir()

	repoCloser := &spySharedClientCloser{}
	searchClientCloser := &spySharedClientCloser{}
	skillsBackend := &customSearchBackend{root: skillsDir, version: "skills-split-v1"}
	knowledgeBackend := &customSearchBackend{root: knowledgeDir, version: "knowledge-split-v1"}

	var composed *workitems.ComposedRepository
	options := testOptions(dir, testConfig(t))
	options.Backends = Backends{
		OpenWorkItems: func(ctx context.Context, _ string) (workitems.Repository, error) {
			store, err := workitems.Open(ctx, workItemsDir)
			if err != nil {
				return nil, err
			}
			composed, err = workitems.ComposeRepository(store, store, store, store, store, store, repoCloser, repoCloser)
			return composed, err
		},
		SkillsSearch:    skillsBackend,
		KnowledgeSearch: knowledgeBackend,
		Closers:         []io.Closer{searchClientCloser, searchClientCloser},
	}

	ctx := t.Context()
	rt, err := EnsureWithOptions(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := rt.Client().CreateWorkspace(ctx, workitems.WorkspaceInput{
		Name:           "split-search-workspace",
		WorkspaceRoots: []string{t.TempDir()},
	})
	if err != nil {
		_ = rt.Close()
		t.Fatal(err)
	}
	if _, err := rt.Client().CreateItem(ctx, workitems.CreateInput{
		WorkspaceID: ws.ID,
		Kind:        workitems.Issue,
		Title:       "Split Search Routing",
		Content:     "Verify SkillsSearch and KnowledgeSearch route to separate backends.",
		Author:      "tester",
	}); err != nil {
		_ = rt.Close()
		t.Fatal(err)
	}
	hits, err := rt.Client().SearchWork(ctx, WorkSearchRequest{
		WorkspaceID: ws.ID,
		Query:       "SkillsSearch",
		Mode:        "lexical",
	})
	if err != nil || len(hits.Hits) != 1 {
		_ = rt.Close()
		t.Fatalf("SearchWork = %#v, err = %v", hits, err)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}

	if got := repoCloser.closes.Load(); got != 1 {
		t.Fatalf("repository closer count = %d, want 1", got)
	}
	if got := searchClientCloser.closes.Load(); got != 1 {
		t.Fatalf("shared search client closer count = %d, want 1", got)
	}
	if len(skillsBackend.specs) != 1 || skillsBackend.specs[0].Purpose != SearchPurposeSkills {
		t.Fatalf("skills backend specs = %#v", skillsBackend.specs)
	}
	if len(knowledgeBackend.specs) == 0 {
		t.Fatal("knowledge backend was not called")
	}
	for _, spec := range knowledgeBackend.specs {
		if spec.Purpose != SearchPurposeKnowledge {
			t.Fatalf("unexpected spec sent to knowledge backend: %#v", spec)
		}
	}
}

type spyWorkItemsRepo struct {
	workitems.Repository
	closes atomic.Int32
}

func (s *spyWorkItemsRepo) Close() error {
	s.closes.Add(1)
	return s.Repository.Close()
}

type spySearchIndex struct {
	sessionstore.SearchIndex
	closes atomic.Int32
}

func (s *spySearchIndex) Close() error {
	s.closes.Add(1)
	return s.SearchIndex.Close()
}

type customSearchBackend struct {
	root    string
	version string

	mu      sync.Mutex
	specs   []SearchIndexSpec
	opened  []*spySearchIndex
	created map[string]bool
}

func (b *customSearchBackend) MappingVersion() string { return b.version }

func (b *customSearchBackend) Open(ctx context.Context, spec SearchIndexSpec) (OpenedSearchIndex, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.specs = append(b.specs, spec)
	if b.created == nil {
		b.created = map[string]bool{}
	}
	key := spec.Purpose + "/" + spec.Generation + "/" + spec.Namespace
	indexRoot := filepath.Join(b.root, spec.Purpose, spec.Namespace)
	if spec.Generation != "" {
		indexRoot = filepath.Join(b.root, spec.Purpose, spec.Generation, spec.Namespace)
	}
	if err := os.MkdirAll(indexRoot, 0o700); err != nil {
		return OpenedSearchIndex{}, err
	}
	firstOpen := !b.created[key]
	b.created[key] = true
	raw, err := sessionstore.OpenLocalIndex(ctx, indexRoot, sessionstore.OpenOptions{Vector: spec.Vector})
	if err != nil {
		return OpenedSearchIndex{}, err
	}
	spy := &spySearchIndex{SearchIndex: raw}
	b.opened = append(b.opened, spy)
	return OpenedSearchIndex{
		Index:   spy,
		Created: spec.Purpose == SearchPurposeKnowledge && firstOpen,
	}, nil
}

func TestEnsureWithCustomBackendsRoutesStorageAndSearch(t *testing.T) {
	dir := t.TempDir()
	workItemsDir := t.TempDir()
	searchDir := t.TempDir()

	var repo *spyWorkItemsRepo
	backend := &customSearchBackend{root: searchDir, version: "opensearch-test-v1"}

	options := testOptions(dir, testConfig(t))
	options.Backends = Backends{
		OpenWorkItems: func(ctx context.Context, _ string) (workitems.Repository, error) {
			store, err := workitems.Open(ctx, workItemsDir)
			if err != nil {
				return nil, err
			}
			repo = &spyWorkItemsRepo{Repository: store}
			return repo, nil
		},
		Search: backend,
	}

	ctx := t.Context()
	rt, err := EnsureWithOptions(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = rt.Close()
		}
	}()

	if repo == nil {
		t.Fatal("custom OpenWorkItems was not invoked")
	}

	ws, err := rt.Client().CreateWorkspace(ctx, workitems.WorkspaceInput{
		Name: "custom-backend-workspace",
		WorkspaceRoots: []string{
			t.TempDir(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := rt.Client().CreateItem(ctx, workitems.CreateInput{
		WorkspaceID: ws.ID,
		Kind:        workitems.Issue,
		Title:       "Custom OpenSearch Backend Proof",
		Content:     "Ensure pluggable repository and search index sync end-to-end.",
		Author:      "tester",
	})
	if err != nil {
		t.Fatal(err)
	}

	results, err := rt.Client().SearchWork(ctx, WorkSearchRequest{
		WorkspaceID: ws.ID,
		Query:       "OpenSearch",
		Mode:        "lexical",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results.Hits) != 1 || results.Hits[0].ItemID != issue.ID {
		t.Fatalf("SearchWork hits = %#v, want item %q", results.Hits, issue.ID)
	}

	defaultBody, _ := json.Marshal([]any{"knowledge-v1", sessionstore.MappingVersion, "", 0})
	defaultDigest := sha256.Sum256(defaultBody)
	defaultGeneration := hex.EncodeToString(defaultDigest[:8])
	if results.Generation == "" || results.Generation == defaultGeneration {
		t.Fatalf("custom search backend generation = %q, want distinct from default %q", results.Generation, defaultGeneration)
	}

	if _, err := os.Stat(filepath.Join(dir, "library", "knowledge-index")); !os.IsNotExist(err) {
		t.Fatalf("default knowledge-index directory should not be created when using custom backend (err=%v)", err)
	}

	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true

	if got := repo.closes.Load(); got != 1 {
		t.Fatalf("repository close count = %d, want 1", got)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	var sawSkills, sawWorkspaceKnowledge bool
	for _, spec := range backend.specs {
		switch spec.Purpose {
		case SearchPurposeSkills:
			if spec.Namespace == "global" {
				sawSkills = true
			}
		case SearchPurposeKnowledge:
			if spec.Namespace == "workspace-"+ws.ID && spec.WorkspaceID == ws.ID && spec.Generation == results.Generation {
				sawWorkspaceKnowledge = true
			}
		}
	}
	if !sawSkills || !sawWorkspaceKnowledge {
		t.Fatalf("missing expected specs: skills=%v knowledge=%v specs=%#v", sawSkills, sawWorkspaceKnowledge, backend.specs)
	}
	for i, idx := range backend.opened {
		if got := idx.closes.Load(); got != 1 {
			t.Fatalf("opened index[%d] close count = %d, want 1", i, got)
		}
	}
}
