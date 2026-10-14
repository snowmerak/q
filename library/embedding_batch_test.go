package library

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	llmclient "github.com/snowmerak/q/client"
	"github.com/snowmerak/q/sessionstore"
)

func TestEmbeddingBatchProjectionAndRecordLimits(t *testing.T) {
	for _, test := range []struct {
		name        string
		counts      []int
		wantRecords int
		wantError   bool
	}{
		{name: "128 projections fit", counts: []int{64, 64}, wantRecords: 2},
		{name: "129 projections split", counts: []int{64, 64, 1}, wantRecords: 2},
		{name: "single record exceeds projection limit", counts: []int{129}, wantError: true},
		{name: "server record limit", counts: make([]int, 33), wantRecords: 32},
	} {
		t.Run(test.name, func(t *testing.T) {
			var items []SkillEmbeddingItem
			for index, count := range test.counts {
				items = append(items, SkillEmbeddingItem{ID: fmt.Sprint(index), Projections: make([]SkillEmbeddingProjection, count)})
			}
			batch, err := nextEmbeddingBatch("model", 3, items)
			if (err != nil) != test.wantError || len(batch.Items) != test.wantRecords {
				t.Fatalf("batch records = %d, error = %v", len(batch.Items), err)
			}
		})
	}
}

func TestEmbeddingBatchJSONByteBoundary(t *testing.T) {
	// Escaped text verifies that the budget measures wire JSON, not string lengths.
	model := "model<\"&\n"
	first := SkillEmbeddingItem{Digest: "digest<\"", Projections: []SkillEmbeddingProjection{{ID: "content", Vector: []float32{-0.000001000001}}}}
	second := SkillEmbeddingItem{ID: "second", Projections: []SkillEmbeddingProjection{{ID: "content", Vector: []float32{1}}}}
	request := SkillEmbeddingApplyRequest{Model: model, Dimensions: 1, Items: []SkillEmbeddingItem{first, second}}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	// 7.2 MiB rounded down to a whole number of bytes, including the envelope.
	const limit = 7_549_747
	first.ID = strings.Repeat("x", limit-len(encoded))
	batch, err := nextEmbeddingBatch(model, 1, []SkillEmbeddingItem{first, second})
	if err != nil || len(batch.Items) != 2 {
		t.Fatalf("exact-limit batch records = %d, error = %v", len(batch.Items), err)
	}
	encoded, err = json.Marshal(batch)
	if err != nil || len(encoded) != limit {
		t.Fatalf("exact-limit JSON = %d bytes, error = %v", len(encoded), err)
	}
	first.ID += "x"
	batch, err = nextEmbeddingBatch(model, 1, []SkillEmbeddingItem{first, second})
	if err != nil || len(batch.Items) != 1 {
		t.Fatalf("one byte over should split: records = %d, error = %v", len(batch.Items), err)
	}
	encoded, err = json.Marshal(batch)
	if err != nil || len(encoded) > limit {
		t.Fatalf("split JSON = %d bytes, error = %v", len(encoded), err)
	}
	// An indivisible record must fail clearly instead of sending an oversized body.
	first.ID += strings.Repeat("x", limit-len(encoded)+1)
	if _, err := nextEmbeddingBatch(model, 1, []SkillEmbeddingItem{first}); err == nil || !strings.Contains(err.Error(), "request bytes") {
		t.Fatalf("oversized record error = %v", err)
	}
}

type largeBatchEmbedder struct{}

func (largeBatchEmbedder) Embed(_ context.Context, request llmclient.EmbeddingRequest) (*llmclient.EmbeddingResponse, error) {
	texts := request.Input.([]string)
	response := &llmclient.EmbeddingResponse{Data: make([]llmclient.Embedding, len(texts))}
	for index := range texts {
		vector := make([]float32, *request.Dimensions)
		for dimension := range vector {
			vector[dimension] = -0.000001000001
		}
		response.Data[index] = llmclient.Embedding{Index: index, Embedding: vector}
	}
	return response, nil
}

func TestGlobalEmbeddingMigrationSplitsLargeJSONBatches(t *testing.T) {
	dir := t.TempDir()
	var records []sessionstore.Record
	for index := 0; index < 32; index++ {
		records = append(records, sessionstore.Record{
			ID: fmt.Sprintf("large-%02d", index), Kind: sessionstore.KindProposition, Scope: "global",
			Content: "Source evidence", Payload: []byte(`{"queries":["first","second","third"]}`),
		})
	}
	seedLibraryRecords(t, dir, records...)
	runtime, err := EnsureWithOptions(t.Context(), testOptions(dir, testConfig(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	client := runtime.Client()
	if err := client.ConfigureEmbedding(largeBatchEmbedder{}, "current", 4096); err != nil {
		t.Fatal(err)
	}
	stats, err := client.SyncEmbeddings(t.Context())
	if err != nil || stats.Propositions != 32 {
		t.Fatalf("large migration = %+v, %v", stats, err)
	}
	stats, err = client.SyncEmbeddings(t.Context())
	if err != nil || stats.Propositions != 0 {
		t.Fatalf("completed migration repeated work = %+v, %v", stats, err)
	}
}
