package embeddingbridge

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

func TestQueryEmbeddingsReuseHostConnectionAndRejectChangedSettings(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer host-key" {
			t.Error("query did not use the host Gateway connection")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"embed","data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	store := config.Store{Dir: dir}
	value := config.Default()
	value.Provider.Model = "test-model"
	value.Embedding = config.EmbeddingConfig{Model: "embed", Dimensions: 2}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runtime", "connection.json")
	if err := Write(path, Connection{Endpoint: server.URL + "/v1", APIKey: "host-key"}); err != nil {
		t.Fatal(err)
	}
	provider := &Embedder{Path: path, Store: store, Model: "embed", Dimensions: 2}
	dimensions := 2
	response, err := provider.Embed(t.Context(), client.EmbeddingRequest{Model: "embed", Input: []string{"query"}, Dimensions: &dimensions})
	if err != nil || len(response.Data) != 1 || calls != 1 {
		t.Fatalf("embedding %+v %v calls=%d", response, err, calls)
	}
	value.Embedding.Model = "changed"
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Embed(t.Context(), client.EmbeddingRequest{Model: "embed", Input: []string{"query"}}); err == nil || calls != 1 {
		t.Fatal("stale settings reached Gateway")
	}
}
