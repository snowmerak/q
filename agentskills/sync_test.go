package agentskills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/snowmerak/q/sessionstore"
)

type pagedSkillRecordStore struct {
	records []sessionstore.Record
	offsets []int
	deleted []string
}

func (s *pagedSkillRecordStore) Search(_ context.Context, options sessionstore.SearchOptions) (sessionstore.SearchResult, error) {
	s.offsets = append(s.offsets, options.Offset)
	start := min(options.Offset, len(s.records))
	end := min(start+options.Limit, len(s.records))
	hits := make([]sessionstore.Hit, 0, end-start)
	for _, record := range s.records[start:end] {
		hits = append(hits, sessionstore.Hit{Record: record})
	}
	return sessionstore.SearchResult{Total: uint64(len(s.records)), Hits: hits}, nil
}

func (s *pagedSkillRecordStore) Save(record sessionstore.Record) (sessionstore.Record, error) {
	return record, nil
}

func (s *pagedSkillRecordStore) Delete(id string) error {
	s.deleted = append(s.deleted, id)
	return nil
}

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

func TestSyncRecordsDeletesStaleRecordsBeyondFirstPage(t *testing.T) {
	store := &pagedSkillRecordStore{records: make([]sessionstore.Record, skillSyncPageSize+1)}
	for index := range store.records {
		store.records[index] = sessionstore.Record{
			ID: fmt.Sprintf("stale-skill-%04d", index), Kind: sessionstore.KindSkill,
		}
	}
	registry := &Registry{skills: make(map[string]Skill)}
	if err := registry.SyncRecords(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(store.offsets, []int{0, skillSyncPageSize}) {
		t.Fatalf("search offsets = %v", store.offsets)
	}
	if len(store.deleted) != skillSyncPageSize+1 {
		t.Fatalf("deleted %d records, want %d", len(store.deleted), skillSyncPageSize+1)
	}
	last := fmt.Sprintf("stale-skill-%04d", skillSyncPageSize)
	if !slices.Contains(store.deleted, last) {
		t.Fatalf("record beyond first page was not deleted: %q", last)
	}
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
