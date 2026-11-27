package opensearchindex

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/sessionstore"
)

func TestNewClientValidationAndIndexNaming(t *testing.T) {
	if _, err := NewClient(Config{}); err == nil {
		t.Fatal("expected error for empty endpoint")
	}
	if _, err := NewClient(Config{Endpoint: "ftp://127.0.0.1:9200"}); err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
	if _, err := NewClient(Config{Endpoint: "http://127.0.0.1:9200", Refresh: "invalid"}); err == nil {
		t.Fatal("expected error for unsupported refresh mode")
	}

	client, err := NewClient(Config{
		Endpoint:    "http://127.0.0.1:9200/",
		IndexPrefix: "My Project/Dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	name := client.IndexName("knowledge/GEN-1/workspace_ABC")
	if !strings.HasPrefix(name, "my-project-dev-") {
		t.Fatalf("unexpected index name %q", name)
	}
	if strings.ContainsAny(name, "ABCDEFGHIJKLMNOPQRSTUVWXYZ/ ") {
		t.Fatalf("index name not sanitized: %q", name)
	}

	longName := client.IndexName(strings.Repeat("very-long-segment-", 25))
	if len(longName) > maximumIndexNameLen {
		t.Fatalf("long index name length = %d, want <= %d", len(longName), maximumIndexNameLen)
	}
}

func TestBuildIndexBodyAndRecordEncoding(t *testing.T) {
	noVec := buildIndexBody(sessionstore.VectorConfig{})
	settings := noVec["settings"].(map[string]any)["index"].(map[string]any)
	if _, hasKNN := settings["knn"]; hasKNN {
		t.Fatalf("non-vector mapping unexpectedly enabled knn: %#v", settings)
	}

	vecCfg := sessionstore.VectorConfig{Model: "embed-v1", Dimensions: 3}
	withVec := buildIndexBody(vecCfg)
	vecSettings := withVec["settings"].(map[string]any)["index"].(map[string]any)
	if enabled, _ := vecSettings["knn"].(bool); !enabled {
		t.Fatalf("vector mapping did not enable knn: %#v", vecSettings)
	}
	props := withVec["mappings"].(map[string]any)["properties"].(map[string]any)
	if _, ok := props["embedding_vec"]; !ok {
		t.Fatal("missing embedding_vec mapping")
	}
	if _, ok := props["vector_projections"]; !ok {
		t.Fatal("missing vector_projections nested mapping")
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	rec := sessionstore.Record{
		ID:          "rec-1",
		Kind:        sessionstore.KindSkill,
		CreatedAt:   now,
		UpdatedAt:   now,
		Summary:     "Skill Title",
		Content:     "Skill Content",
		Tags:        []string{"go", "search"},
		Payload:     []byte(`{"nested":[1,2,3]}`),
		Embedding:   &sessionstore.Embedding{Model: "other-model", Dimensions: 2, CreatedAt: now, Vector: []float32{1, 0}},
		VectorProjections: []sessionstore.VectorProjection{
			{ID: "name", Embedding: sessionstore.Embedding{Model: "embed-v1", Dimensions: 3, CreatedAt: now, Vector: []float32{1, 0, 0}}},
		},
	}
	doc, err := encodeRecordDoc(rec, vecCfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.EmbeddingVec) != 0 {
		t.Fatalf("mismatched model embedding should not be indexed: %#v", doc.EmbeddingVec)
	}
	if len(doc.VectorProjections) != 1 || doc.VectorProjections[0].ID != "name" {
		t.Fatalf("encoded vector projections = %#v", doc.VectorProjections)
	}
	decoded, err := decodeRecordRaw(doc.RecordRaw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Payload, rec.Payload) || decoded.Embedding.Model != "other-model" {
		t.Fatalf("lossy round-trip: got=%#v want=%#v", decoded, rec)
	}
}

func TestBuildBoolQueryAndComposeIndexCompatibility(t *testing.T) {
	opts := sessionstore.SearchOptions{
		Text:       "hello world",
		TextBoosts: &sessionstore.TextFieldBoosts{Summary: 2, Content: 1, SearchText: 0},
		Filters: sessionstore.Filters{
			WorkspaceIDs: []string{"ws-1"},
			Tags:         []string{"a", "b"},
		},
	}
	if err := sessionstore.ValidateSearchOptions(&opts); err != nil {
		t.Fatal(err)
	}
	q := buildBoolQuery(opts, true)
	b, ok := q["bool"].(map[string]any)
	if !ok || len(b["must"].([]map[string]any)) != 1 || len(b["filter"].([]map[string]any)) != 2 {
		t.Fatalf("unexpected bool query: %#v", q)
	}

	idx := &Index{
		recordWriter:   &recordWriter{},
		recordReader:   &recordReader{},
		recordSearcher: &recordSearcher{},
		vectorManager:  &vectorManager{},
		state:          &indexState{},
	}
	composed, err := sessionstore.ComposeIndex(idx.recordWriter, idx.recordReader, idx.recordSearcher, idx.vectorManager, idx)
	if err != nil {
		t.Fatal(err)
	}
	if err := composed.Close(); err != nil {
		t.Fatal(err)
	}
	if client, _ := NewClient(Config{Endpoint: "http://127.0.0.1:9200", HTTPClient: http.DefaultClient}); client == nil {
		t.Fatal("expected valid client")
	}
}
