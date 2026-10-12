package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	llmclient "github.com/snowmerak/q/client"
	"github.com/snowmerak/q/sessionstore"
)

func TestSemanticUseRepairsGlobalEmbeddingsWithoutExplicitSync(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "cat-care")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: cat-care\ndescription: Care for cats.\n---\n\nCats.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedLibraryRecords(t, dir, sessionstore.Record{
		ID: "prop", Kind: sessionstore.KindProposition, Scope: "global", Content: "Cats nap.",
		Embedding: &sessionstore.Embedding{Model: "previous", Dimensions: 2, Vector: []float32{0, 1}},
	})
	runtime, err := EnsureWithOptions(t.Context(), testOptions(dir, testConfig(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	client := runtime.Client()
	embedder := &migrationEmbedder{failAt: 1}
	if err := client.ConfigureEmbedding(embedder, "current", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SearchSkills(t.Context(), SkillSearchRequest{Query: "cat"}); err == nil {
		t.Fatal("failed skill repair was hidden")
	}
	embedder.failAt = 0
	if result, err := client.SearchSkills(t.Context(), SkillSearchRequest{Query: "semantic-only"}); err != nil || len(result.Hits) != 1 {
		t.Fatalf("automatic skill repair = %#v, %v", result, err)
	}
	if result, err := client.SearchPropositions(t.Context(), PropositionSearchRequest{Query: "semantic-only"}); err != nil || len(result.Hits) != 1 {
		t.Fatalf("automatic proposition repair = %#v, %v", result, err)
	}
	check := func(id, model string, dimensions int) {
		t.Helper()
		record, err := runtime.leader.archive.Get(id)
		if err != nil || record.Embedding != nil || len(record.VectorProjections) == 0 {
			t.Fatalf("repaired record = %#v, %v", record, err)
		}
		for _, projection := range record.VectorProjections {
			if projection.Embedding.Model != model || projection.Embedding.Dimensions != dimensions {
				t.Fatalf("outdated projection = %#v", projection)
			}
		}
	}
	check("prop", "current", 3)
	calls := embedder.calls
	if _, err := client.SearchPropositions(t.Context(), PropositionSearchRequest{Query: "semantic-only"}); err != nil {
		t.Fatal(err)
	}
	if embedder.calls != calls+1 {
		t.Fatal("matching records were embedded again")
	}
	if _, err := runtime.leader.archive.Save(sessionstore.Record{
		ID: "imported", Kind: sessionstore.KindProposition, Scope: "global", Content: "A later import.",
	}); err != nil {
		t.Fatal(err)
	}
	embedder.failAt = embedder.calls + 1
	if _, err := client.SearchPropositions(t.Context(), PropositionSearchRequest{Query: "semantic-only"}); err == nil {
		t.Fatal("failed late-import repair was hidden")
	}
	embedder.failAt = 0
	if _, err := client.SearchPropositions(t.Context(), PropositionSearchRequest{Query: "semantic-only"}); err != nil {
		t.Fatal(err)
	}
	check("imported", "current", 3)
	if err := client.ConfigureEmbedding(embedder, "next", 4); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SearchPropositions(t.Context(), PropositionSearchRequest{Query: "semantic-only"}); err != nil {
		t.Fatal(err)
	}
	check("prop", "next", 4)
	check("imported", "next", 4)
	// Registering with a different active graph also repairs it on use.
	if err := client.ConfigureEmbedding(embedder, "registration-model", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RegisterProposition(t.Context(), "repair-on-register", PropositionRegisterRequest{
		Content: "An unrelated proposition.", Confidence: 0.8,
		ExtractorModel: "fixture", ExtractorVersion: "v1",
	}); err != nil {
		t.Fatal(err)
	}
	check("prop", "registration-model", 3)
}

type blockedMigrationEmbedder struct {
	started chan struct{}
}

func (e *blockedMigrationEmbedder) Embed(ctx context.Context, _ llmclient.EmbeddingRequest) (*llmclient.EmbeddingResponse, error) {
	close(e.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestGlobalRepairWaitingRequestCanCancel(t *testing.T) {
	dir := t.TempDir()
	seedLibraryRecords(t, dir, sessionstore.Record{
		ID: "prop", Kind: sessionstore.KindProposition, Scope: "global", Content: "Cats nap.",
	})
	runtime, err := EnsureWithOptions(t.Context(), testOptions(dir, testConfig(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	client := runtime.Client()
	embedder := &blockedMigrationEmbedder{started: make(chan struct{})}
	if err := client.ConfigureEmbedding(embedder, "current", 3); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.SearchPropositions(ctx, PropositionSearchRequest{Query: "cat"})
		done <- err
	}()
	select {
	case <-embedder.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer waitCancel()
	if _, err := client.SearchPropositions(waitCtx, PropositionSearchRequest{Query: "cat"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting request error = %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("repair cancellation = %v", err)
	}
}
