package outbox

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPEmbedderPrefixesDimensionsAndAuth(t *testing.T) {
	var seen []struct {
		Auth       string `json:"auth"`
		Model      string `json:"model"`
		Input      string `json:"input"`
		Dimensions int    `json:"dimensions"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Model      string `json:"model"`
			Input      string `json:"input"`
			Dimensions int    `json:"dimensions"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatal(err)
		}
		seen = append(seen, struct {
			Auth       string `json:"auth"`
			Model      string `json:"model"`
			Input      string `json:"input"`
			Dimensions int    `json:"dimensions"`
		}{
			Auth:       r.Header.Get("Authorization"),
			Model:      req.Model,
			Input:      req.Input,
			Dimensions: req.Dimensions,
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.25,0.5,0.75]}]}`)
	}))
	defer srv.Close()

	emb, err := NewHTTPEmbedder(HTTPEmbedderConfig{
		BaseURL:        srv.URL + "/v1",
		APIKeyEnv:      "TEST_EMB_KEY",
		Model:          "qwen-emb",
		Dimensions:     3,
		QueryPrefix:    "query: ",
		DocumentPrefix: "passage: ",
		LookupEnv:      func(string) string { return "secret-token" },
	})
	if err != nil {
		t.Fatalf("NewHTTPEmbedder: %v", err)
	}

	model, dim, vec, err := emb.EmbedDocument(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("EmbedDocument: %v", err)
	}
	if model != "qwen-emb" || dim != 3 || len(vec) != 3 {
		t.Fatalf("unexpected doc embedding: model=%s dim=%d vec=%v", model, dim, vec)
	}

	qvec, err := emb.EmbedQuery(context.Background(), "find hello")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	if len(qvec) != 3 {
		t.Fatalf("unexpected query vector len: %d", len(qvec))
	}

	if len(seen) != 2 {
		t.Fatalf("requests = %d, want 2", len(seen))
	}
	if seen[0].Auth != "Bearer secret-token" || seen[0].Input != "passage: hello world" || seen[0].Dimensions != 3 {
		t.Fatalf("doc request mismatch: %+v", seen[0])
	}
	if seen[1].Input != "query: find hello" {
		t.Fatalf("query request mismatch: %+v", seen[1])
	}
}

func TestHTTPEmbedderValidationAndErrorHandling(t *testing.T) {
	if _, err := NewHTTPEmbedder(HTTPEmbedderConfig{Model: "m"}); err == nil {
		t.Fatal("expected error when base_url is missing")
	}
	if _, err := NewHTTPEmbedder(HTTPEmbedderConfig{BaseURL: "http://localhost", APIKeyEnv: "MISSING_ENV", Model: "m", LookupEnv: func(string) string { return "" }}); err == nil {
		t.Fatal("expected error when APIKeyEnv resolves empty")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failure", http.StatusBadGateway)
	}))
	defer srv.Close()

	emb, err := NewHTTPEmbedder(HTTPEmbedderConfig{BaseURL: srv.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emb.EmbedQuery(context.Background(), "hi"); err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("expected HTTP 502 error, got %v", err)
	}
}
