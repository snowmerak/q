package workspacememory

import (
	"errors"
	"testing"

	"github.com/snowmerak/q/sessionstore"
)

func TestRemoteEmbeddingPatchPreservesChangesAndDeletions(t *testing.T) {
	runtime, err := EnsureWithOptions(t.Context(), testEnsureOptions(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	archive, err := runtime.Client().OpenWorkspace(t.Context(), t.TempDir(), sessionstore.VectorConfig{Model: "current", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = archive.Close() })
	first, err := archive.Save(sessionstore.Record{ID: "changed", Kind: sessionstore.KindMessage, Content: "old"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := archive.Save(sessionstore.Record{ID: "deleted", Kind: sessionstore.KindMessage, Content: "removed"})
	if err != nil {
		t.Fatal(err)
	}
	updates := make([]sessionstore.EmbeddingUpdate, 0, 2)
	for _, record := range []sessionstore.Record{first, second} {
		digest, err := sessionstore.RecordSourceDigest(record)
		if err != nil {
			t.Fatal(err)
		}
		updates = append(updates, sessionstore.EmbeddingUpdate{ID: record.ID, SourceDigest: digest, Embedding: &sessionstore.Embedding{Model: "current", Dimensions: 2, Vector: []float32{1, 0}}})
	}
	first.Content = "new"
	if _, err := archive.Save(first); err != nil {
		t.Fatal(err)
	}
	if err := archive.Delete(second.ID); err != nil {
		t.Fatal(err)
	}
	result, err := archive.ApplyEmbeddingsContext(t.Context(), updates)
	if err != nil || result.Updated != 0 || result.Skipped != 2 {
		t.Fatalf("stale patches = %#v, %v", result, err)
	}
	latest, err := archive.Get(first.ID)
	if err != nil || latest.Content != "new" || latest.Embedding != nil {
		t.Fatalf("source edit overwritten: %#v, %v", latest, err)
	}
	if _, err := archive.Get(second.ID); !errors.Is(err, sessionstore.ErrNotFound) {
		t.Fatalf("deleted record resurrected: %v", err)
	}
	digest, err := sessionstore.RecordSourceDigest(latest)
	if err != nil {
		t.Fatal(err)
	}
	updates[0].SourceDigest = digest
	result, err = archive.ApplyEmbeddingsContext(t.Context(), updates[:1])
	if err != nil || result.Updated != 1 {
		t.Fatalf("fresh patch = %#v, %v", result, err)
	}
}
