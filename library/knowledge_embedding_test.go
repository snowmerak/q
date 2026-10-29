package library

import (
	"net/http"
	"testing"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workitems"
)

func TestEmbeddingModelAndDimensionsRebuildOnlyDerivedKnowledge(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
	settings := config.Store{Dir: dir}
	value := config.Default()
	value.Provider.Model = "test-model"
	value.Embedding = config.EmbeddingConfig{Model: "old-embedding", Dimensions: 2}
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	runtime, err := EnsureWithOptions(ctx, testOptions(dir, testConfig(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	c := runtime.Client()
	w, err := c.CreateWorkspace(ctx, workitems.WorkspaceInput{Name: "Generation", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := c.CreateItem(ctx, workitems.CreateInput{WorkspaceID: w.ID, Kind: workitems.Issue, Title: "Generation", Content: "Stable original evidence", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	query := WorkSearchRequest{WorkspaceID: w.ID, Mode: "lexical", Query: "evidence"}
	before, err := c.SearchWork(ctx, query)
	if err != nil || len(before.Hits) != 1 {
		t.Fatalf("initial projection: %+v %v", before, err)
	}
	var batch WorkEmbeddingBatch
	if err := c.doJSONWithClient(ctx, c.index, http.MethodGet, "/work/embeddings", nil, &batch, nil); err != nil || len(batch.Sources) != 1 {
		t.Fatalf("initial batch: %+v %v", batch, err)
	}
	value.Embedding = config.EmbeddingConfig{Model: "new-embedding", Dimensions: 3}
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	var ack struct {
		Updated int `json:"updated"`
	}
	if err := c.doJSONWithClient(ctx, c.index, http.MethodPost, "/work/embeddings", WorkEmbeddingApply{Model: batch.Model, Dimensions: batch.Dimensions, Sources: batch.Sources, Vectors: [][]float32{{1, 0}}}, &ack, nil); err == nil {
		t.Fatal("a result from the previous embedding generation was accepted")
	}
	after, err := c.SearchWork(ctx, query)
	if err != nil || len(after.Hits) != 1 || after.Generation == before.Generation || !after.EmbeddingPending {
		t.Fatalf("replacement generation: %+v %v", after, err)
	}
	content, err := c.ReadContent(ctx, item.BodyPostID, "", 100)
	if err != nil || content.Post.Content != "Stable original evidence" || content.Post.Revision != 1 {
		t.Fatalf("embedding change mutated originals: %+v %v", content, err)
	}
}

func TestKnowledgeEmbeddingsUseWorkspacePartitionAndRejectStaleSources(t *testing.T) {
	ctx := t.Context()
	options := testOptions(t.TempDir(), testConfig(t))
	options.Vector = sessionstore.VectorConfig{Model: "knowledge-test", Dimensions: 2}
	runtime, err := EnsureWithOptions(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	c := runtime.Client()
	local, err := c.CreateWorkspace(ctx, workitems.WorkspaceInput{Name: "Small", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := c.CreateWorkspace(ctx, workitems.WorkspaceInput{Name: "Large", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.CreateItem(ctx, workitems.CreateInput{WorkspaceID: local.ID, Kind: workitems.Issue, Title: "Decision", Content: "Vector evidence", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 20; n++ {
		if _, err := c.CreateItem(ctx, workitems.CreateInput{WorkspaceID: other.ID, Kind: workitems.Issue, Title: "Distractor", Content: "Other workspace evidence", Author: "user"}); err != nil {
			t.Fatal(err)
		}
	}
	query := WorkSearchRequest{WorkspaceID: local.ID, Mode: "semantic", EmbeddingModel: "knowledge-test", Embedding: []float32{1, 0}, Limit: 5}
	if _, err := c.SearchWork(ctx, query); err == nil {
		t.Fatal("semantic query hid an empty vector index")
	}
	for batchNumber := 0; batchNumber < 8; batchNumber++ {
		var batch WorkEmbeddingBatch
		if err := c.doJSONWithClient(ctx, c.index, http.MethodGet, "/work/embeddings", nil, &batch, nil); err != nil {
			t.Fatal(err)
		}
		if len(batch.Sources) == 0 {
			break
		}
		vectors := make([][]float32, len(batch.Sources))
		for i := range vectors {
			vectors[i] = []float32{1, 0}
		}
		var ack struct {
			Updated int `json:"updated"`
		}
		if err := c.doJSONWithClient(ctx, c.index, http.MethodPost, "/work/embeddings", WorkEmbeddingApply{Model: batch.Model, Dimensions: batch.Dimensions, Sources: batch.Sources, Vectors: vectors}, &ack, nil); err != nil || ack.Updated != len(batch.Sources) {
			t.Fatalf("apply %+v %v", ack, err)
		}
	}
	result, err := c.SearchWork(ctx, query)
	if err != nil || len(result.Hits) != 1 || result.Hits[0].ItemID != first.ID || result.EmbeddingPending {
		t.Fatalf("local ANN partition %+v %v", result, err)
	}
	if _, err := c.UpdatePost(ctx, first.BodyPostID, workitems.PostInput{Content: "New source", Author: "user", Revision: 1}); err != nil {
		t.Fatal(err)
	}
	var batch WorkEmbeddingBatch
	if err := c.doJSONWithClient(ctx, c.index, http.MethodGet, "/work/embeddings", nil, &batch, nil); err != nil {
		t.Fatal(err)
	}
	if len(batch.Sources) != 1 {
		t.Fatalf("pending sources %+v", batch)
	}
	if _, err := c.UpdatePost(ctx, first.BodyPostID, workitems.PostInput{Content: "Changed again", Author: "user", Revision: 2}); err != nil {
		t.Fatal(err)
	}
	var ack struct {
		Updated int `json:"updated"`
	}
	if err := c.doJSONWithClient(ctx, c.index, http.MethodPost, "/work/embeddings", WorkEmbeddingApply{Model: batch.Model, Dimensions: batch.Dimensions, Sources: batch.Sources, Vectors: [][]float32{{1, 0}}}, &ack, nil); err != nil || ack.Updated != 0 {
		t.Fatalf("stale result applied %+v %v", ack, err)
	}
	if _, err := c.SearchWork(ctx, query); err == nil {
		t.Fatal("stale vector remained searchable after source replacement")
	}
	content, err := c.ReadContent(ctx, first.BodyPostID, "", 100)
	if err != nil || content.Post.Content != "Changed again" {
		t.Fatalf("embedding changed originals %+v %v", content, err)
	}
}
