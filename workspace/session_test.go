package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestStoreRoundTripAndClear(t *testing.T) {
	store := Store{Root: t.TempDir()}
	want := Session{
		Transcript: []client.Message{{Role: client.RoleUser, Content: "question"}},
		Context:    []client.Message{{Role: client.RoleSystem, Name: "q_context_summary", Content: "summary"}},
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != CurrentVersion || len(got.Transcript) != 1 || got.Transcript[0].Content != "question" ||
		len(got.Context) != 1 || got.Context[0].Content != "summary" {
		t.Fatalf("loaded session = %#v", got)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("session permissions are too broad: %o", info.Mode().Perm())
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load() after Clear() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, DirectoryName)); err != nil {
		t.Fatalf("Clear removed the workspace directory: %v", err)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), []byte(`{"version":1,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load accepted an unknown field")
	}
}
