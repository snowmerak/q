package agentskills

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/snowmerak/q/sessionstore"
)

func TestSyncRecordsTracksAddUpdateAndDelete(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	directory := writeSkill(t, root, ".agents", "indexed-skill", "Initial unique description.")
	registry, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sessionstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := registry.SyncRecords(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	assertSkillSearchCount(t, store, "Initial unique", 1)

	body := "---\nname: indexed-skill\ndescription: Updated searchable phrase.\ntags: [special-tag]\n---\n\nUpdated.\n"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := registry.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := registry.SyncRecords(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	assertSkillSearchCount(t, store, "Updated searchable", 1)
	assertSkillSearchCount(t, store, "special tag", 1)

	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	if err := registry.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := registry.SyncRecords(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	assertSkillSearchCount(t, store, "Updated searchable", 0)
}

func assertSkillSearchCount(t *testing.T, store *sessionstore.Store, query string, want int) {
	t.Helper()
	result, err := store.Search(context.Background(), sessionstore.SearchOptions{
		Text: query, Filters: sessionstore.Filters{Kinds: []string{sessionstore.KindSkill}}, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != want {
		t.Fatalf("query %q hits=%#v want=%d", query, result.Hits, want)
	}
}
