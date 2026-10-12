package app

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/internal/hostruntime"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workspacememory"
)

func TestSessionHostMigratesAllCatalogArchivesWithoutSelectedRoot(t *testing.T) {
	settings := config.Store{Dir: t.TempDir()}
	freePort := func() int {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		return port
	}
	if err := (qlibrary.ConfigStore{Dir: settings.Dir}).Save(qlibrary.Config{Host: "127.0.0.1", Port: freePort()}); err != nil {
		t.Fatal(err)
	}
	if err := (workspacememory.ConfigStore{Dir: settings.Dir}).Save(workspacememory.Config{Host: "127.0.0.1", Port: freePort()}); err != nil {
		t.Fatal(err)
	}
	roots := []string{t.TempDir(), t.TempDir()}
	for _, root := range roots {
		store, err := sessionstore.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.Save(sessionstore.Record{
			ID: "message", Kind: sessionstore.KindMessage, Content: "Stored history.",
			Embedding: &sessionstore.Embedding{Model: "old-model", Dimensions: 2, Vector: []float32{1, 0}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	emptyRoot := t.TempDir()
	host := &SessionHost{
		ctx: t.Context(), runtime: &hostruntime.Runtime{}, store: settings, providerReady: true,
		factory: func(config.Config) (chatClient, error) { return &extractionClient{}, nil },
	}
	host.SetEmbeddingWorkspaceResolver(func() ([]string, error) {
		return []string{roots[0], roots[1], roots[0], emptyRoot}, nil
	})
	value := config.Default()
	value.Provider.Model = "test-model"
	value.Embedding = config.EmbeddingConfig{Model: "new-model", Dimensions: 3}
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	if err := host.SyncEmbeddings(t.Context(), "", value); err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		store, err := sessionstore.OpenWithOptions(root, sessionstore.OpenOptions{Vector: sessionstore.VectorConfig{Model: "new-model", Dimensions: 3}})
		if err != nil {
			t.Fatal(err)
		}
		record, err := store.Get("message")
		if err != nil || record.Embedding == nil || record.Embedding.Model != "new-model" || len(record.Embedding.Vector) != 3 {
			t.Fatalf("workspace %q record = %#v, %v", root, record, err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(emptyRoot, ".q")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration created an unused archive: %v", err)
	}
}

func TestManualEmbeddingSyncUsesSavedSettingsAndOnlySelectedArchive(t *testing.T) {
	settings := config.Store{Dir: t.TempDir()}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	if err := (workspacememory.ConfigStore{Dir: settings.Dir}).Save(workspacememory.Config{Host: "127.0.0.1", Port: port}); err != nil {
		t.Fatal(err)
	}
	roots := []string{t.TempDir(), t.TempDir()}
	for _, root := range roots {
		store, err := sessionstore.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Save(sessionstore.Record{ID: "record", Kind: "message", Content: "source", Embedding: &sessionstore.Embedding{Model: "old", Dimensions: 2, Vector: []float32{1, 0}}}); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	value := config.Default()
	value.Provider.Model = "test"
	value.Embedding = config.EmbeddingConfig{Model: "saved", Dimensions: 3}
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	provider := &extractionClient{}
	host := &SessionHost{ctx: t.Context(), runtime: &hostruntime.Runtime{}, store: settings, providerReady: true,
		factory: func(value config.Config) (chatClient, error) {
			if value.Embedding.Model != "saved" {
				t.Errorf("client configured from obsolete model: %s", value.Embedding.Model)
			}
			return provider, nil
		}}
	host.SetEmbeddingWorkspaceResolver(func() ([]string, error) { return roots, nil })
	embedded, err := host.SyncEmbeddingArchive(t.Context(), roots[0])
	if err != nil || embedded != 1 {
		t.Fatalf("sync = %d, %v", embedded, err)
	}
	for index, root := range roots {
		status, err := os.ReadFile(filepath.Join(root, ".q", "index", "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		store, err := sessionstore.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		record, err := store.Get("record")
		if err != nil {
			t.Fatal(err)
		}
		want := "saved"
		if index == 1 {
			want = "old"
		}
		if record.Embedding.Model != want {
			t.Fatalf("root %d model = %s; metadata %s", index, record.Embedding.Model, status)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(settings.Dir, "library")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace sync opened unrelated Library: %v", err)
	}
	missing := t.TempDir()
	if _, err := host.SyncEmbeddingArchive(t.Context(), missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing archive = %v", err)
	}
	if _, err := os.Stat(filepath.Join(missing, ".q")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manual sync created missing archive: %v", err)
	}
}

type changingEmbeddingProvider struct {
	extractionClient
	change func()
}

func (provider *changingEmbeddingProvider) Embed(ctx context.Context, request client.EmbeddingRequest) (*client.EmbeddingResponse, error) {
	response, err := provider.extractionClient.Embed(ctx, request)
	provider.change()
	return response, err
}

func TestManualEmbeddingProviderRejectsModelChangeDuringProviderCall(t *testing.T) {
	settings := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test"
	value.Embedding = config.EmbeddingConfig{Model: "current", Dimensions: 3}
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	provider := &changingEmbeddingProvider{change: func() {
		value.Embedding.Model = "newer"
		if err := settings.Save(value); err != nil {
			t.Fatal(err)
		}
	}}
	guard := &currentEmbeddingProvider{store: settings, model: "current", dimensions: 3, provider: provider}
	dimensions := 3
	if response, err := guard.Embed(t.Context(), client.EmbeddingRequest{Model: "current", Dimensions: &dimensions, Input: []string{"source"}}); err == nil || response != nil {
		t.Fatalf("superseded provider result escaped guard: %+v, %v", response, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if err := guard.check(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled guard = %v", err)
	}
}
