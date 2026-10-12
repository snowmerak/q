package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	llmclient "github.com/snowmerak/q/client"
	"github.com/snowmerak/q/sessionstore"
)

type migrationEmbedder struct {
	calls  int
	failAt int
}

func (e *migrationEmbedder) Embed(_ context.Context, request llmclient.EmbeddingRequest) (*llmclient.EmbeddingResponse, error) {
	e.calls++
	if e.calls == e.failAt {
		return nil, errors.New("embedding fixture unavailable")
	}
	texts := request.Input.([]string)
	result := &llmclient.EmbeddingResponse{Data: make([]llmclient.Embedding, len(texts))}
	for index := range texts {
		vector := make([]float32, *request.Dimensions)
		vector[0] = 1
		result.Data[index] = llmclient.Embedding{Index: index, Embedding: vector}
	}
	return result, nil
}

func TestGlobalEmbeddingMigrationResumesAndPreservesPropositions(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "cat-care")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: cat-care\ndescription: Care for cats.\n---\n\nCats.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	var records []sessionstore.Record
	for index := 0; index < 105; index++ {
		records = append(records, sessionstore.Record{
			ID: fmt.Sprintf("prop-%03d", index), Kind: sessionstore.KindProposition, Scope: "global",
			Content: "Care for cats.", SearchText: "cat care\nfeline care", Tags: []string{"cats"}, Refs: []string{"session://source"},
			CreatedAt: now, UpdatedAt: now,
			Payload:   []byte(`{"queries":["cat care","feline care"],"confidence":0.8,"extractor_model":"original","extractor_version":"v1","idempotency_hash":"retained","request_digest":"retained"}`),
			Embedding: &sessionstore.Embedding{Model: "old-model", Dimensions: 2, Vector: []float32{1, 0}},
		})
	}
	seedLibraryRecords(t, dir, records...)
	runtime, err := EnsureWithOptions(t.Context(), testOptions(dir, testConfig(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	before, err := runtime.leader.archive.Get("prop-000")
	if err != nil {
		t.Fatal(err)
	}
	client := runtime.Client()
	embedder := &migrationEmbedder{failAt: 6}
	if err := client.ConfigureEmbedding(embedder, "new-model", 3); err != nil {
		t.Fatal(err)
	}
	stats, err := client.SyncEmbeddings(t.Context())
	if err == nil || stats.Skills != 1 || stats.Propositions != 32 {
		t.Fatalf("interrupted migration = %#v, %v", stats, err)
	}
	embedder.failAt = 0
	stats, err = client.SyncEmbeddings(t.Context())
	if err != nil || stats.Skills != 0 || stats.Propositions != 73 {
		t.Fatalf("resumed migration = %#v, %v", stats, err)
	}
	for index := 0; index < 105; index++ {
		record, err := runtime.leader.archive.Get(fmt.Sprintf("prop-%03d", index))
		if err != nil || record.Embedding != nil || len(record.VectorProjections) != 3 {
			t.Fatalf("migrated proposition = %#v, %v", record, err)
		}
		for _, projection := range record.VectorProjections {
			if projection.Embedding.Model != "new-model" || projection.Embedding.Dimensions != 3 {
				t.Fatalf("outdated projection = %#v", projection)
			}
		}
	}
	after, err := runtime.leader.archive.Get(before.ID)
	if err != nil {
		t.Fatal(err)
	}
	after.Embedding, after.VectorProjections = before.Embedding, before.VectorProjections
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed source metadata: before %#v, after %#v", before, after)
	}
	calls := embedder.calls
	if stats, err := client.SyncEmbeddings(t.Context()); err != nil || stats != (EmbeddingSyncStats{}) || embedder.calls != calls {
		t.Fatalf("unchanged migration = %#v, %v; embedding calls %d -> %d", stats, err, calls, embedder.calls)
	}
	if err := client.ConfigureEmbedding(embedder, "another-model", 4); err != nil {
		t.Fatal(err)
	}
	if stats, err := client.SyncEmbeddings(t.Context()); err != nil || stats.Skills != 1 || stats.Propositions != 105 {
		t.Fatalf("model and dimension migration = %#v, %v", stats, err)
	}
	result, err := client.SearchPropositions(t.Context(), PropositionSearchRequest{Query: "semantic-only-query"})
	if err != nil || len(result.Hits) == 0 {
		t.Fatalf("semantic search after migration = %#v, %v", result, err)
	}
}

func TestPropositionEmbeddingRejectsChangedSource(t *testing.T) {
	dir := t.TempDir()
	seedLibraryRecords(t, dir, sessionstore.Record{
		ID: "prop", Kind: sessionstore.KindProposition, Scope: "global", Content: "Original fact.",
	})
	runtime, err := EnsureWithOptions(t.Context(), testOptions(dir, testConfig(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	archive := runtime.leader.archive
	if err := archive.ConfigureVector(sessionstore.VectorConfig{Model: "new-model", Dimensions: 3}); err != nil {
		t.Fatal(err)
	}
	record, err := archive.Get("prop")
	if err != nil {
		t.Fatal(err)
	}
	source, err := propositionEmbeddingSource(record)
	if err != nil {
		t.Fatal(err)
	}
	record.Content = "Updated fact."
	if _, err := archive.Save(record); err != nil {
		t.Fatal(err)
	}
	_, err = runtime.leader.propositions.applyEmbeddings(SkillEmbeddingApplyRequest{
		Model: "new-model", Dimensions: 3, Items: []SkillEmbeddingItem{{
			ID: "prop", Digest: source.Digest,
			Projections: []SkillEmbeddingProjection{{ID: "content", Vector: []float32{1, 0, 0}}},
		}},
	})
	if err == nil {
		t.Fatal("stale embedding overwrote a changed proposition")
	}
}
