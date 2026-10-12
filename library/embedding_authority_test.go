package library

import (
	"os"
	"path/filepath"
	"testing"

	qconfig "github.com/snowmerak/q/config"
	"github.com/snowmerak/q/sessionstore"
)

func TestOldClientFollowsSavedModelAndRepairsOldQueryVectors(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "cat-care")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: cat-care\ndescription: Care for cats.\n---\n\nCats.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedLibraryRecords(t, dir, sessionstore.Record{ID: "prop", Kind: sessionstore.KindProposition, Scope: "global", Content: "Cats nap."})
	settings := qconfig.Store{Dir: dir}
	value := qconfig.Default()
	value.Provider.Model = "fixture"
	value.Embedding = qconfig.EmbeddingConfig{Model: "old", Dimensions: 3}
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	runtime, err := EnsureWithOptions(t.Context(), testOptions(dir, testConfig(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	oldClient := runtime.Client()
	provider := &migrationEmbedder{}
	if err := oldClient.ConfigureEmbedding(provider, "old", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := oldClient.SyncEmbeddings(t.Context()); err != nil {
		t.Fatal(err)
	}
	value.Embedding = qconfig.EmbeddingConfig{Model: "new", Dimensions: 4}
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	query := SkillSearchRequest{Query: "semantic-only", EmbeddingModel: "old", Embedding: []float32{1, 0, 0}}
	if result, err := oldClient.SearchSkills(t.Context(), query); err != nil || len(result.Hits) != 1 {
		t.Fatalf("old client search = %#v, %v", result, err)
	}
	if graph := runtime.leader.archive.VectorConfig(); graph.Model != "new" || graph.Dimensions != 4 {
		t.Fatalf("old client rolled graph back = %#v", graph)
	}
	if config := oldClient.embeddingConfig(); config.model != "new" || config.dimensions != 4 {
		t.Fatalf("old client did not adopt saved model = %#v", config)
	}
	if result, err := oldClient.SearchPropositions(t.Context(), PropositionSearchRequest{Query: "semantic-only", EmbeddingModel: "old", Embedding: []float32{1, 0, 0}}); err != nil || len(result.Hits) != 1 {
		t.Fatalf("old proposition query = %#v, %v", result, err)
	}
	if _, err := oldClient.SearchSkills(t.Context(), SkillSearchRequest{EmbeddingModel: "old", Embedding: []float32{1, 0, 0}}); err == nil {
		t.Fatal("unrepairable vector silently became lexical search")
	}
	// Stored disabling and re-enabling also override an older client snapshot.
	value.Embedding = qconfig.EmbeddingConfig{}
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	if _, err := oldClient.SearchSkills(t.Context(), query); err != nil {
		t.Fatal(err)
	}
	if runtime.leader.archive.VectorConfig().Enabled() {
		t.Fatal("old client re-enabled a disabled model")
	}
	value.Embedding = qconfig.EmbeddingConfig{Model: "reenabled", Dimensions: 3}
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	if result, err := oldClient.SearchSkills(t.Context(), query); err != nil || len(result.Hits) != 1 {
		t.Fatalf("automatic re-enable = %#v, %v", result, err)
	}
	if graph := runtime.leader.archive.VectorConfig(); graph.Model != "reenabled" {
		t.Fatalf("re-enable graph = %#v", graph)
	}
}
