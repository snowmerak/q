package workspacememory

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/q/sessionstore"
)

type spyWorkspaceSearchIndex struct {
	sessionstore.SearchIndex
	saves      atomic.Int32
	gets       atomic.Int32
	searches   atomic.Int32
	configures atomic.Int32
	closes     atomic.Int32
	nilCtxSeen atomic.Bool
}

func (s *spyWorkspaceSearchIndex) checkCtx(ctx context.Context) {
	if ctx == nil {
		s.nilCtxSeen.Store(true)
	}
}

func (s *spyWorkspaceSearchIndex) Save(ctx context.Context, record sessionstore.Record) (sessionstore.Record, error) {
	s.checkCtx(ctx)
	s.saves.Add(1)
	return s.SearchIndex.Save(ctx, record)
}

func (s *spyWorkspaceSearchIndex) Get(ctx context.Context, id string) (sessionstore.Record, error) {
	s.checkCtx(ctx)
	s.gets.Add(1)
	return s.SearchIndex.Get(ctx, id)
}

func (s *spyWorkspaceSearchIndex) Search(ctx context.Context, options sessionstore.SearchOptions) (sessionstore.SearchResult, error) {
	s.checkCtx(ctx)
	s.searches.Add(1)
	return s.SearchIndex.Search(ctx, options)
}

func (s *spyWorkspaceSearchIndex) ConfigureVector(ctx context.Context, config sessionstore.VectorConfig) error {
	s.checkCtx(ctx)
	s.configures.Add(1)
	return s.SearchIndex.ConfigureVector(ctx, config)
}

func (s *spyWorkspaceSearchIndex) Close() error {
	s.closes.Add(1)
	return s.SearchIndex.Close()
}

func TestEnsureWithCustomStoreOpenerRoutesIndexOperations(t *testing.T) {
	dir := t.TempDir()
	workspaceRoot := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		canonicalRoot = workspaceRoot
	}
	canonicalRoot = filepath.Clean(canonicalRoot)

	var openedRoot string
	var spy *spyWorkspaceSearchIndex

	rt, err := EnsureWithOptions(t.Context(), EnsureOptions{
		Dir:            dir,
		Config:         testConfig(t),
		ProbeTimeout:   300 * time.Millisecond,
		StartupTimeout: 3 * time.Second,
		OpenStore: func(ctx context.Context, root string, options sessionstore.OpenOptions) (sessionstore.SearchIndex, error) {
			openedRoot = root
			idx, err := sessionstore.OpenLocalIndex(ctx, root, options)
			if err != nil {
				return nil, err
			}
			spy = &spyWorkspaceSearchIndex{SearchIndex: idx}
			return spy, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Error(err)
		}
	}()

	ws, err := rt.Client().OpenWorkspace(t.Context(), workspaceRoot, sessionstore.VectorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if openedRoot != canonicalRoot || spy == nil {
		t.Fatalf("openedRoot = %q, want %q, spy=%v", openedRoot, canonicalRoot, spy != nil)
	}

	saved, err := ws.Save(sessionstore.Record{Kind: sessionstore.KindMessage, Content: "custom workspace search index"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ws.Get(saved.ID)
	if err != nil || got.ID != saved.ID {
		t.Fatalf("Get = %#v, err = %v", got, err)
	}
	res, err := ws.Search(t.Context(), sessionstore.SearchOptions{Text: "custom"})
	if err != nil || len(res.Hits) != 1 {
		t.Fatalf("Search = %#v, err = %v", res, err)
	}
	if err := ws.ConfigureVector(sessionstore.VectorConfig{Model: "embed", Dimensions: 3}); err != nil {
		t.Fatal(err)
	}
	if err := ws.Close(); err != nil {
		t.Fatal(err)
	}

	if spy.nilCtxSeen.Load() {
		t.Fatal("observed nil context passed into SearchIndex")
	}
	if spy.saves.Load() != 1 || spy.gets.Load() != 1 || spy.searches.Load() != 1 || spy.configures.Load() != 1 || spy.closes.Load() != 1 {
		t.Fatalf("unexpected counts: saves=%d gets=%d searches=%d configures=%d closes=%d",
			spy.saves.Load(), spy.gets.Load(), spy.searches.Load(), spy.configures.Load(), spy.closes.Load())
	}
}
