package config

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/llm/gateway"
)

func TestLoadDefaultWhenMissingAndSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate default: %v", err)
	}
	cfg.Tools.ApprovalMode = domain.ApprovalRequest
	cfg.LLM.Providers = []gateway.ProviderConfig{
		{ID: "local", Type: "openai-compatible", Prefix: "local", Enabled: true, BaseURL: "http://127.0.0.1:11888/v1", APIKey: "inline-secret"},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load saved: %v", err)
	}
	if loaded.Tools.ApprovalMode != domain.ApprovalRequest {
		t.Fatalf("ApprovalMode = %q, want request", loaded.Tools.ApprovalMode)
	}
	redacted := loaded.Redact()
	if redacted.LLM.Providers[0].APIKey != "" {
		t.Fatalf("expected redacted APIKey to be empty")
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte(`{"version":2,"unknown_top_level":true}`))
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestDiffRestartRequired(t *testing.T) {
	a := Default()
	b := Default()
	b.Tools.ApprovalMode = domain.ApprovalBypass
	if fields := DiffRestartRequired(a, b); len(fields) != 0 {
		t.Fatalf("expected no restart fields, got %v", fields)
	}
	b.Server.Listen = "127.0.0.1:19090"
	b.Storage.RDB.Driver = "postgres"
	fields := DiffRestartRequired(a, b)
	if len(fields) != 2 {
		t.Fatalf("expected 2 restart fields, got %v", fields)
	}
}

func TestWatcherRetainsPreviousConfigOnInvalidWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	initial := Default()
	if err := Save(path, initial); err != nil {
		t.Fatalf("Save initial: %v", err)
	}

	var changed atomic.Int32
	var errored atomic.Int32
	w, err := StartWatcher(context.Background(), path, initial, func(_, _ Config) {
		changed.Add(1)
	}, func(error) {
		errored.Add(1)
	})
	if err != nil {
		t.Fatalf("StartWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Invalid JSON must trigger error without replacing Current().
	if err := os.WriteFile(path, []byte(`{invalid json`), 0o600); err != nil {
		t.Fatalf("write invalid json: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && errored.Load() == 0 {
		time.Sleep(50 * time.Millisecond)
	}
	if errored.Load() == 0 {
		t.Fatal("expected watcher error callback on invalid JSON")
	}
	if got := w.Current().Tools.ApprovalMode; got != domain.ApprovalAutoDetection {
		t.Fatalf("expected previous valid config preserved, got %q", got)
	}

	// Valid update must apply cleanly.
	updated := Default()
	updated.Tools.ApprovalMode = domain.ApprovalBypass
	if err := Save(path, updated); err != nil {
		t.Fatalf("Save updated: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && changed.Load() == 0 {
		time.Sleep(50 * time.Millisecond)
	}
	if changed.Load() == 0 || w.Current().Tools.ApprovalMode != domain.ApprovalBypass {
		t.Fatalf("expected hot-reload to apply ApprovalBypass, changed=%d current=%q", changed.Load(), w.Current().Tools.ApprovalMode)
	}
}
