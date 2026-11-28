package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/snowmerak/q/v2/internal/fsutil"
	"github.com/snowmerak/q/v2/pkg/app"
	"github.com/snowmerak/q/v2/pkg/app/config"
)

func TestAppLifecycleEndpointsAndSingleInstanceLock(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	a, err := app.Open(ctx, app.Options{DataDir: dataDir, ListenOverride: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("app.Open: %v", err)
	}
	defer func() { _ = a.Close(ctx) }()

	// Second Open on the same data directory must be rejected by instance.lock.
	if _, err := app.Open(ctx, app.Options{DataDir: dataDir}); !errors.Is(err, fsutil.ErrLocked) {
		t.Fatalf("expected fsutil.ErrLocked on duplicate Open, got %v", err)
	}

	h := a.Handler()

	// GET /api/v1/health
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected X-Request-ID response header")
	}

	// GET /api/v1/config
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get config status = %d", rec.Code)
	}

	// PUT /api/v1/config
	cfg := config.Default()
	cfg.Tools.ApprovalMode = "request"
	body, _ := json.Marshal(cfg)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("put config status = %d body=%s", rec.Code, rec.Body.String())
	}

	// GET /api/v1/models and /api/v1/decisions/models
	for _, path := range []string{"/api/v1/models", "/api/v1/decisions/models"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", path, rec.Code, rec.Body.String())
		}
	}

	// Unmatched /api/ route returns JSON 404, not HTML.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/does-not-exist", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	var errBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil || errBody.Error.Code != "not_found" {
		t.Fatalf("expected JSON not_found error, got %s", rec.Body.String())
	}
}
