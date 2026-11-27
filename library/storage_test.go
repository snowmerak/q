package library

import (
	"testing"

	"github.com/snowmerak/q/storageconfig"
)

func TestResolveBackends(t *testing.T) {
	dir := t.TempDir()
	// Default / missing config -> local.
	b, err := ResolveBackends(dir)
	if err != nil {
		t.Fatalf("ResolveBackends(default): %v", err)
	}
	if b.OpenWorkItems != nil || b.Search != nil || b.SkillsSearch != nil || b.KnowledgeSearch != nil || len(b.Closers) != 0 {
		t.Fatalf("expected empty local backends, got %#v", b)
	}

	// Remote mode with only Knowledge on OpenSearch keeps Skills local.
	cfg := storageconfig.Config{
		Mode: storageconfig.ModeRemote,
		Remote: storageconfig.RemoteConfig{
			OpenSearch: storageconfig.OpenSearchConfig{
				Enabled:  true,
				Endpoint: "http://127.0.0.1:9200",
				Targets: storageconfig.OpenSearchTargets{
					Skills:          false,
					Knowledge:       true,
					WorkspaceMemory: false,
				},
			},
			Postgres: storageconfig.PostgresConfig{
				Enabled: false,
			},
		},
	}
	if err := (storageconfig.Store{Dir: dir}).Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err = ResolveBackends(dir)
	if err != nil {
		t.Fatalf("ResolveBackends(partial remote): %v", err)
	}
	if b.OpenWorkItems != nil || b.Search != nil || b.KnowledgeSearch == nil || b.SkillsSearch == nil || len(b.Closers) != 1 {
		t.Fatalf("unexpected split remote backends: %#v", b)
	}
	for _, c := range b.Closers {
		_ = c.Close()
	}

	// Full remote with Postgres enabled without DSN errors cleanly.
	cfg.Remote.Postgres.Enabled = true
	if err := (storageconfig.Store{Dir: dir}).Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := ResolveBackends(dir); err == nil {
		t.Fatal("expected error when remote postgres is enabled without DSN")
	}

	// Full remote with both skills & knowledge sets unified Search + Postgres opener.
	cfg.Remote.Postgres.DSN = "postgres://postgres:qtest@127.0.0.1:5432/qtest?sslmode=disable"
	cfg.Remote.OpenSearch.Targets.Skills = true
	if err := (storageconfig.Store{Dir: dir}).Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err = ResolveBackends(dir)
	if err != nil {
		t.Fatalf("ResolveBackends(full remote): %v", err)
	}
	if b.OpenWorkItems == nil || b.Search == nil || len(b.Closers) != 1 {
		t.Fatalf("expected unified remote Search + OpenWorkItems, got %#v", b)
	}
	for _, c := range b.Closers {
		_ = c.Close()
	}
}
