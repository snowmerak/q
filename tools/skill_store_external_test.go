package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/tools/builtin"
)

type memorySkillStore struct {
	records map[string]sessionstore.Record
}

var _ tools.SkillStore = (*memorySkillStore)(nil)

func (s *memorySkillStore) Search(_ context.Context, options sessionstore.SearchOptions) (sessionstore.SearchResult, error) {
	var records []sessionstore.Record
	for _, record := range s.records {
		if !matchesAny(record.Kind, options.Filters.Kinds) || !matchesAny(record.Scope, options.Filters.Scopes) ||
			!matchesTags(record.Tags, options.Filters.Tags) {
			continue
		}
		searchable := strings.ToLower(strings.Join([]string{record.Summary, record.Content, record.SearchText}, " "))
		if query := strings.ToLower(strings.TrimSpace(options.Text)); query != "" && !strings.Contains(searchable, query) {
			continue
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	result := sessionstore.SearchResult{Total: uint64(len(records))}
	start := min(options.Offset, len(records))
	end := len(records)
	if options.Limit > 0 {
		end = min(start+options.Limit, end)
	}
	for _, record := range records[start:end] {
		result.Hits = append(result.Hits, sessionstore.Hit{Record: record, Score: 1})
	}
	return result, nil
}

func (s *memorySkillStore) Save(record sessionstore.Record) (sessionstore.Record, error) {
	if s.records == nil {
		s.records = make(map[string]sessionstore.Record)
	}
	s.records[record.ID] = record
	return record, nil
}

func (s *memorySkillStore) Delete(id string) error {
	delete(s.records, id)
	return nil
}

func matchesAny(value string, values []string) bool {
	if len(values) == 0 {
		return true
	}
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func matchesTags(recordTags, wanted []string) bool {
	if len(wanted) == 0 {
		return true
	}
	for _, recordTag := range recordTags {
		for _, wantedTag := range wanted {
			if recordTag == wantedTag {
				return true
			}
		}
	}
	return false
}

func TestRuntimeUsesInjectedSkillStoreWithoutArchive(t *testing.T) {
	root := t.TempDir()
	skillDirectory := filepath.Join(root, ".agents", "skills", "embedded-workflow")
	if err := os.MkdirAll(skillDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	const skillBody = "---\nname: embedded-workflow\ndescription: Use the unique embedded workflow signal.\n---\n\nFollow the embedded workflow exactly.\n"
	if err := os.WriteFile(filepath.Join(skillDirectory, "SKILL.md"), []byte(skillBody), 0o644); err != nil {
		t.Fatal(err)
	}

	runtime, err := tools.NewRuntimeWithSkillStore(t.Context(), root, &memorySkillStore{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()

	for _, name := range []string{"search_skills", "get_skill"} {
		if !hasTool(runtime, name) {
			t.Fatalf("injected Skill Store did not enable %q", name)
		}
	}
	for _, name := range []string{"search_archive", "get_archive_record"} {
		if hasTool(runtime, name) {
			t.Fatalf("Skill Store unexpectedly enabled archive tool %q", name)
		}
	}

	hints, err := runtime.SearchSkillHints(t.Context(), "unique embedded workflow signal", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(hints.Hits) != 1 || hints.Hits[0].Title != "embedded-workflow" {
		t.Fatalf("skill hints = %#v", hints)
	}

	searchResult, err := runtime.Call(t.Context(), client.ToolCall{
		ID: "search", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "search_skills", Arguments: `{"query":"unique embedded workflow signal","scopes":["workspace"]}`},
	})
	if err != nil || searchResult.IsError || !strings.Contains(searchResult.Content, "embedded-workflow") {
		t.Fatalf("search_skills result = %#v, err = %v", searchResult, err)
	}

	getResult, err := runtime.Call(t.Context(), client.ToolCall{
		ID: "get", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "get_skill", Arguments: `{"id":"` + hints.Hits[0].ID + `"}`},
	})
	if err != nil || getResult.IsError {
		t.Fatalf("get_skill result = %#v, err = %v", getResult, err)
	}
	var got builtin.GetSkillOutput
	if err := json.Unmarshal([]byte(getResult.Content), &got); err != nil {
		t.Fatal(err)
	}
	if got.Skill.Name != "embedded-workflow" || got.Content != skillBody {
		t.Fatalf("get_skill output = %#v", got)
	}
}

func hasTool(runtime *tools.Runtime, name string) bool {
	for _, tool := range runtime.Tools() {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}
