package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/sessionstore"
)

func TestEmbeddingStatusReportsCoverageWithoutRepairingLockedArchives(t *testing.T) {
	base := t.TempDir()
	configDirectory, root, sibling, absent := filepath.Join(base, "settings"), filepath.Join(base, "project"), filepath.Join(base, "unregistered"), filepath.Join(base, "empty")
	for _, directory := range []string{configDirectory, root, sibling, absent} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	main := config.Store{Dir: configDirectory}
	value := config.Default()
	value.Provider.Model = "test"
	value.Embedding = config.EmbeddingConfig{Model: "current", Dimensions: 3}
	if err := main.Save(value); err != nil {
		t.Fatal(err)
	}
	old := sessionstore.VectorConfig{Model: "old", Dimensions: 2}
	archive, err := sessionstore.OpenWithOptions(root, sessionstore.OpenOptions{Vector: old})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = archive.Close() })
	now := time.Now().UTC()
	current := &sessionstore.Embedding{Model: "current", Dimensions: 3, Vector: []float32{1, 0, 0}, CreatedAt: now}
	previous := &sessionstore.Embedding{Model: "old", Dimensions: 2, Vector: []float32{1, 0}, CreatedAt: now}
	for _, record := range []sessionstore.Record{
		{ID: "current", Kind: "message", Content: "current", Embedding: current},
		{ID: "old", Kind: "message", Content: "old", Embedding: previous},
		{ID: "missing", Kind: "message", Content: "missing"},
		{ID: "excluded", Kind: "message", Content: "excluded", Tags: []string{"archive-read"}},
		{ID: "empty", Kind: "event"},
		{ID: "partial-skill", Kind: "skill", Summary: "skill", Content: "description", VectorProjections: []sessionstore.VectorProjection{{ID: "name", Embedding: *current}}},
	} {
		if _, err := archive.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	other, err := sessionstore.Open(sibling)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Save(sessionstore.Record{ID: "other", Kind: "message", Content: "unregistered"}); err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	global, err := sessionstore.OpenWithOptions(configDirectory, sessionstore.OpenOptions{Directory: "library", Vector: old})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = global.Close() })
	if _, err := global.Save(sessionstore.Record{ID: "prop", Kind: "proposition", Scope: "global", Content: "proposition", Payload: json.RawMessage(`{"queries":["query"]}`), VectorProjections: []sessionstore.VectorProjection{{ID: "content", Embedding: *current}}}); err != nil {
		t.Fatal(err)
	}
	projects := newStudioProjectStore(configDirectory)
	if _, err := projects.create(studioProjectUpdateRequest{Name: "project", WorkspaceRoots: []string{root, absent}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, ".q", "index", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(main)
	if err != nil {
		t.Fatal(err)
	}
	response := serveJSON(t, handler, http.MethodGet, "/api/v1/embeddings/status", nil)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status = %d %s", response.Code, response.Body.String())
	}
	var result embeddingStatusResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Complete || !result.Enabled || result.Model != "current" || len(result.Archives) != 4 {
		t.Fatalf("snapshot = %+v", result)
	}
	for _, status := range result.Archives {
		switch status.WorkspaceRoot {
		case root:
			if !status.Registered || status.State != "pending" || status.Current != 1 || status.Missing != 1 || status.Stale != 2 || status.Excluded != 2 || status.CurrentVectors != 2 || status.Index.ConfigurationMatches {
				t.Fatalf("project status = %+v", status)
			}
		case sibling:
			if status.Registered || status.Missing != 1 || status.State != "pending" {
				t.Fatalf("unregistered status = %+v", status)
			}
		case absent:
			if status.Exists || status.State != "absent" {
				t.Fatalf("absent status = %+v", status)
			}
		case "":
			if status.Scope != "global" || status.Stale != 1 || status.Eligible != 1 {
				t.Fatalf("global partial projections = %+v", status)
			}
		default:
			t.Fatalf("unexpected archive %+v", status)
		}
	}
	after, err := os.ReadFile(filepath.Join(root, ".q", "index", "state.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("status changed index metadata: %v", err)
	}
	if _, err := os.Stat(filepath.Join(absent, ".q")); !os.IsNotExist(err) {
		t.Fatalf("status created absent archive: %v", err)
	}
	record, err := archive.Get("old")
	if err != nil || record.Embedding.Model != "old" {
		t.Fatalf("status migrated source: %+v, %v", record, err)
	}
	// The extra path is validated and can inspect archives outside discovery.
	invalid := serveJSON(t, handler, http.MethodGet, "/api/v1/embeddings/status?workspace_root="+url.QueryEscape(filepath.Join(base, "not-found")), nil)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid root = %d", invalid.Code)
	}
	if response := serveJSON(t, handler, http.MethodGet, "/api/v1/embeddings/status?workspace_root=relative", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("relative root = %d", response.Code)
	}
	extra := filepath.Join(base, "outside", "workspace")
	if err := os.MkdirAll(extra, 0o700); err != nil {
		t.Fatal(err)
	}
	response = serveJSON(t, handler, http.MethodGet, "/api/v1/embeddings/status?workspace_root="+url.QueryEscape(extra), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("extra root = %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, archive := range result.Archives {
		if archive.WorkspaceRoot == extra && !archive.Registered && archive.State == "absent" {
			found = true
		}
	}
	if !found {
		t.Fatalf("extra workspace absent from result: %+v", result)
	}
}

func TestEmbeddingStatusRecognizesCurrentCoverageAndIndex(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	main := config.Store{Dir: filepath.Join(base, "settings")}
	value := config.Default()
	value.Provider.Model = "test"
	value.Embedding = config.EmbeddingConfig{Model: "current", Dimensions: 2}
	if err := main.Save(value); err != nil {
		t.Fatal(err)
	}
	archive, err := sessionstore.OpenWithOptions(root, sessionstore.OpenOptions{Vector: sessionstore.VectorConfig{Model: "current", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = archive.Close() })
	if _, err := archive.Save(sessionstore.Record{ID: "current", Kind: "message", Content: "current", Embedding: &sessionstore.Embedding{Model: "current", Dimensions: 2, Vector: []float32{1, 0}, CreatedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	service := embeddingStatusService{main: main, sessions: newSessionsService(t.Context(), nil, main.Dir)}
	result, err := service.snapshot(t.Context(), root)
	if err != nil || !result.Complete || len(result.Archives) != 2 || result.Archives[1].State != "current" || result.Archives[1].Current != 1 || result.Archives[1].Vectors != 1 {
		t.Fatalf("current snapshot = %+v, %v", result, err)
	}
}

func TestEmbeddingStatusShowsReadFailuresAndDisabledConfiguration(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	directory := filepath.Join(root, ".q", "data", "records")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	main := config.Store{Dir: filepath.Join(base, "settings")}
	value := config.Default()
	value.Provider.Model = "test"
	value.Embedding = config.EmbeddingConfig{}
	if err := main.Save(value); err != nil {
		t.Fatal(err)
	}
	service := embeddingStatusService{main: main, sessions: newSessionsService(t.Context(), nil, main.Dir)}
	result, err := service.snapshot(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Enabled || len(result.Archives) != 2 || result.Archives[1].State != "error" || result.Archives[1].Unreadable != 1 || len(result.Archives[1].Issues) == 0 {
		t.Fatalf("partial scan = %+v", result)
	}
	if err := os.Remove(filepath.Join(directory, "broken.json")); err != nil {
		t.Fatal(err)
	}
	result, err = service.snapshot(t.Context(), root)
	if err != nil || !result.Complete || result.Archives[1].State != "disabled" {
		t.Fatalf("disabled scan = %+v, %v", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := service.snapshot(ctx, root); err != context.Canceled {
		t.Fatalf("canceled scan = %v", err)
	}
}
