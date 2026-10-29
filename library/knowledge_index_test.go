package library

import (
	"net/http"
	"testing"

	"github.com/snowmerak/q/workitems"
)

func TestKnowledgeScopeSourceRevisionsAndReindex(t *testing.T) {
	dir := t.TempDir()
	runtime, err := EnsureWithOptions(t.Context(), testOptions(dir, testConfig(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	client := runtime.Client()
	ctx := t.Context()
	w1, err := client.CreateWorkspace(ctx, workitems.WorkspaceInput{Name: "One", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	w2, err := client.CreateWorkspace(ctx, workitems.WorkspaceInput{Name: "Two", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	var first workitems.Item
	for _, w := range []workitems.Workspace{w1, w2} {
		item, err := client.CreateItem(ctx, workitems.CreateInput{WorkspaceID: w.ID, Kind: workitems.Issue, Title: "Knowledge", Content: "quartz indexing decision", Author: "user"})
		if err != nil {
			t.Fatal(err)
		}
		if w.ID == w1.ID {
			first = item
		}
	}
	local, err := client.SearchWork(ctx, WorkSearchRequest{WorkspaceID: w1.ID, Query: "quartz", Mode: "lexical"})
	if err != nil || len(local.Hits) != 1 || local.Hits[0].WorkspaceID != w1.ID {
		t.Fatalf("local scope: %+v %v", local, err)
	}
	all, err := client.SearchWork(ctx, WorkSearchRequest{All: true, Query: "quartz", Mode: "hybrid"})
	if err != nil || len(all.Hits) != 2 || all.Fallback != "embedding_unavailable" {
		t.Fatalf("global: %+v %v", all, err)
	}
	if _, err := client.SearchWork(ctx, WorkSearchRequest{WorkspaceID: w1.ID, Query: "quartz", Mode: "semantic"}); err == nil {
		t.Fatal("semantic silently fell back")
	}
	if _, err := client.UpdatePost(ctx, first.BodyPostID, workitems.PostInput{Content: "revised source", Author: "user", Revision: 1}); err != nil {
		t.Fatal(err)
	}
	local, err = client.SearchWork(ctx, WorkSearchRequest{WorkspaceID: w1.ID, Query: "quartz", Mode: "lexical"})
	if err != nil || len(local.Hits) != 0 {
		t.Fatalf("stale index hit: %+v %v", local, err)
	}
	var ack struct {
		Queued bool `json:"queued"`
	}
	if err := client.doJSON(ctx, http.MethodPost, "/work/reindex", struct{}{}, &ack); err != nil || !ack.Queued {
		t.Fatalf("reindex: %v", err)
	}
	local, err = client.SearchWork(ctx, WorkSearchRequest{WorkspaceID: w1.ID, Query: "revised", Mode: "lexical"})
	if err != nil || len(local.Hits) != 1 || local.Hits[0].Revision != 2 {
		t.Fatalf("reindex source: %+v %v", local, err)
	}
	body, err := client.ReadContent(ctx, first.BodyPostID, "", 100)
	if err != nil || body.Post.Content != "revised source" {
		t.Fatalf("reindex changed original: %+v %v", body, err)
	}
}
