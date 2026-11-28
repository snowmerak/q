package knowledge_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/q/v2/pkg/knowledge"
	"github.com/snowmerak/q/v2/pkg/storage/outbox"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/sqlite"
	"github.com/snowmerak/q/v2/pkg/storage/search/blevehnsw"
)

func TestKnowledgeLifecycleAndSearch(t *testing.T) {
	dir := t.TempDir()
	ctx := t.Context()
	rdb, err := sqlite.Open(ctx, filepath.Join(dir, "q.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()

	idx, err := blevehnsw.Open(filepath.Join(dir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	worker := outbox.NewWorker(rdb, idx, nil)
	svc := knowledge.NewService(rdb, idx, nil)

	doc, err := svc.CreateDoc(ctx, knowledge.CreateDocInput{
		Scope:   "global",
		Title:   "Harbor Field Manual",
		Content: "Keep the brass lantern lit at dusk on Pier Seven.\nAlways double check tide tables before departure.",
	})
	if err != nil {
		t.Fatalf("CreateDoc: %v", err)
	}
	if n, err := worker.ProcessOnce(ctx); err != nil || n == 0 {
		t.Fatalf("outbox ProcessOnce n=%d err=%v", n, err)
	}

	hits, err := svc.Search(ctx, knowledge.SearchInput{
		Query:  "brass lantern Pier Seven",
		Scopes: []string{"global"},
		Limit:  5,
	})
	if err != nil || len(hits) != 1 || hits[0].DocID != doc.ID {
		t.Fatalf("Search hits=%+v err=%v", hits, err)
	}

	// Update doc -> new revision replaces stale chunk.
	newText := "The northern beacon uses green oil after midnight."
	updated, err := svc.UpdateDoc(ctx, doc.ID, knowledge.UpdateDocInput{
		ExpectedRevision: doc.Revision,
		Content:          &newText,
	})
	if err != nil || updated.Revision != 2 {
		t.Fatalf("UpdateDoc rev=%d err=%v", updated.Revision, err)
	}
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}

	hitsOld, err := svc.Search(ctx, knowledge.SearchInput{Query: "brass lantern", Limit: 5})
	if err != nil || len(hitsOld) != 0 {
		t.Fatalf("expected old chunk deleted after update, got %+v err=%v", hitsOld, err)
	}
	hitsNew, err := svc.Search(ctx, knowledge.SearchInput{Query: "green oil beacon", Limit: 5})
	if err != nil || len(hitsNew) != 1 || !strings.Contains(hitsNew[0].Text, "green oil") {
		t.Fatalf("expected updated chunk match, got %+v err=%v", hitsNew, err)
	}

	// Delete doc -> search returns empty.
	if err := svc.DeleteDoc(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	hitsDeleted, err := svc.Search(ctx, knowledge.SearchInput{Query: "green oil beacon", Limit: 5})
	if err != nil || len(hitsDeleted) != 0 {
		t.Fatalf("expected empty hits after delete, got %+v err=%v", hitsDeleted, err)
	}
}
