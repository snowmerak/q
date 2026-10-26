package composer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/snowmerak/q/workspace"
)

func TestComposerUsesCanonicalPrivateSessionAndExplicitRoot(t *testing.T) {
	config := t.TempDir()
	store := NewStore(config)
	c, err := store.Create("Idea", "provider/model")
	if err != nil {
		t.Fatal(err)
	}
	native := store.SessionStore(c)
	if c.ID != c.SessionID || filepath.Dir(native.Root) != filepath.Join(config, "chats") {
		t.Fatalf("private session=%+v", c)
	}
	if native.SessionDir() != filepath.Join(native.Root, ".q", "sessions", c.ID) {
		t.Fatal("noncanonical composer session directory")
	}
	value, err := native.Load()
	if err != nil || value.Type != workspace.Composer || value.Title != c.Name {
		t.Fatalf("parent=%+v %v", value, err)
	}
	files := &Artifacts{Root: native.SessionDir()}
	if _, err := files.Write("docs/diagram/idea.d2", "user -> idea", nil); err != nil {
		t.Fatal(err)
	}
	explicitRoot := t.TempDir()
	second, err := store.CreateInRoot(explicitRoot, "Other idea", "provider/model")
	if err != nil {
		t.Fatal(err)
	}
	if second.WorkspaceRoot != explicitRoot {
		t.Fatal("explicit storage root ignored")
	}
	ordinary, lock, err := workspace.CreateSession(explicitRoot, "ordinary")
	if err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()
	if err := store.Delete(second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ordinary.Load(); err != nil {
		t.Fatal("typed delete affected an ordinary session")
	}
	if _, err := os.Stat(explicitRoot); err != nil {
		t.Fatal("typed delete removed its workspace")
	}
	listed, err := NewStore(config).List()
	if err != nil || len(listed) != 1 || listed[0].ID != c.ID {
		t.Fatalf("reopened catalog=%+v %v", listed, err)
	}
}

func TestComposerDoesNotImportLegacyData(t *testing.T) {
	config := t.TempDir()
	old := filepath.Join(config, "composer", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "composer.json"), []byte("old conversation"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := NewStore(config).List()
	if err != nil || len(list) != 0 {
		t.Fatalf("imported old layout=%+v %v", list, err)
	}
}
