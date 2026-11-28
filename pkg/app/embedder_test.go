package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/snowmerak/q/v2/pkg/app/config"
	"github.com/snowmerak/q/v2/pkg/llm/gateway"
)

func TestGatewayEmbedderDocumentAndQuery(t *testing.T) {
	var seenModels []string
	var seenInputs []string
	var seenDims []int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"text-embedding-3-small","object":"model"}]}`))
		case "/v1/embeddings":
			var body struct {
				Model      string `json:"model"`
				Input      string `json:"input"`
				Dimensions int    `json:"dimensions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode embedding request: %v", err)
			}
			seenModels = append(seenModels, body.Model)
			seenInputs = append(seenInputs, body.Input)
			seenDims = append(seenDims, body.Dimensions)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.25,0.5,-0.75]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	providerCfg := gateway.ProviderConfig{
		ID:      "openai",
		Type:    "openai-compatible",
		Prefix:  "openai",
		Enabled: true,
		BaseURL: srv.URL + "/v1",
		APIKey:  "test-key",
	}
	gw, err := gateway.NewContext(context.Background(), gateway.Config{
		Providers: []gateway.ProviderConfig{providerCfg},
	})
	if err != nil {
		t.Fatalf("gateway.NewContext: %v", err)
	}
	mg := newManagedGateway(gw)
	defer mg.Release()

	emb, err := newGatewayEmbedder(mg, providerCfg, config.EmbeddingsConfig{
		Enabled:        true,
		ProviderID:     "openai",
		Model:          "text-embedding-3-small",
		Dimensions:     3,
		QueryPrefix:    "query: ",
		DocumentPrefix: "passage: ",
		TimeoutMS:      5000,
	})
	if err != nil {
		t.Fatalf("newGatewayEmbedder: %v", err)
	}

	model, dim, docVec, err := emb.EmbedDocument(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("EmbedDocument: %v", err)
	}
	if model != "openai/text-embedding-3-small" || dim != 3 || len(docVec) != 3 {
		t.Fatalf("unexpected doc embed result: model=%q dim=%d vec=%v", model, dim, docVec)
	}

	queryVec, err := emb.EmbedQuery(context.Background(), "find hello")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	if len(queryVec) != 3 || queryVec[0] != 0.25 || queryVec[2] != -0.75 {
		t.Fatalf("unexpected query vector: %v", queryVec)
	}

	if len(seenModels) != 2 || seenModels[0] != "text-embedding-3-small" || seenModels[1] != "text-embedding-3-small" {
		t.Fatalf("expected stripped upstream model name, got %v", seenModels)
	}
	if seenInputs[0] != "passage: hello world" || seenInputs[1] != "query: find hello" {
		t.Fatalf("expected prefixes applied, got %v", seenInputs)
	}
	if seenDims[0] != 3 || seenDims[1] != 3 {
		t.Fatalf("expected dimensions forwarded, got %v", seenDims)
	}
}
