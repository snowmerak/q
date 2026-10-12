package app

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

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
