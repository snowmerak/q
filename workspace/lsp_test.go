package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/snowmerak/q/lsp"
)

func TestLSPSettingsRoundTrip(t *testing.T) {
	store := Store{Root: t.TempDir()}
	global := lsp.GlobalConfig{Servers: map[string]lsp.ServerConfig{"gopls": {Languages: []string{"go"}, Command: "gopls"}}}
	want := lsp.WorkspaceConfig{Roots: []lsp.RootConfig{{Path: "services/api", Language: "go", Server: "gopls"}}}
	if err := store.SaveLSP(want, global); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadLSP()
	if err != nil {
		t.Fatal(err)
	}
	want.Version = lsp.WorkspaceConfigVersion
	want.Roots[0].Source = lsp.RootSourceManual
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded = %#v, want %#v", got, want)
	}
}

func TestDiscoverLSPRootsFoldsWorkspacesAndHonorsIgnore(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.work", "go 1.26\nuse ./services/api\n")
	write("services/api/go.mod", "module api\n")
	write("crates/Cargo.toml", "[workspace]\nmembers = [\"one\"]\n")
	write("crates/one/Cargo.toml", "[package]\nname=\"one\"\n")
	write("web/tsconfig.json", `{}`)
	write("python/pyproject.toml", "[project]\nname='p'\n")
	write("ignored/tsconfig.json", `{}`)
	write(".qignore", "ignored/\n")
	roots, err := (Store{Root: root}).DiscoverLSPRoots()
	if err != nil {
		t.Fatal(err)
	}
	want := []lsp.RootConfig{
		{Path: ".", Language: "go", Source: lsp.RootSourceDiscovered},
		{Path: "crates", Language: "rust", Source: lsp.RootSourceDiscovered},
		{Path: "python", Language: "python", Source: lsp.RootSourceDiscovered},
		{Path: "web", Language: "typescript", Source: lsp.RootSourceDiscovered},
	}
	if !reflect.DeepEqual(roots, want) {
		t.Fatalf("roots = %#v, want %#v", roots, want)
	}
}

func TestDiscoverLSPRootsHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (Store{Root: t.TempDir()}).DiscoverLSPRootsContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled discovery = %v", err)
	}
}
