package storageconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreLoadOrDefaultAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: dir}

	if _, err := store.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load missing = %v, want ErrNotFound", err)
	}
	def, err := store.LoadOrDefault()
	if err != nil {
		t.Fatalf("LoadOrDefault: %v", err)
	}
	if def.Mode != ModeLocal || def.Remote.OpenSearch.IndexPrefix != "q" || def.Remote.OpenSearch.Refresh != "wait_for" {
		t.Fatalf("unexpected default config: %#v", def)
	}

	want := Config{
		Mode: ModeRemote,
		Remote: RemoteConfig{
			OpenSearch: OpenSearchConfig{
				Enabled:        true,
				Endpoint:       "http://127.0.0.1:9200/",
				IndexPrefix:    "team-q",
				Refresh:        "true",
				Username:       "alice",
				PasswordEnv:    "OS_PASS",
				BearerTokenEnv: "OS_TOK",
				Targets: OpenSearchTargets{
					Skills:          false,
					Knowledge:       true,
					WorkspaceMemory: true,
				},
			},
			Postgres: PostgresConfig{
				Enabled: true,
				DSNEnv:  "PG_DSN",
			},
		},
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Mode != ModeRemote || got.Remote.OpenSearch.Endpoint != "http://127.0.0.1:9200" {
		t.Fatalf("unexpected loaded config: %#v", got)
	}
	if got.UseRemoteOpenSearch(TargetSkills) {
		t.Fatal("expected skills target to stay local")
	}
	if !got.UseRemoteOpenSearch(TargetKnowledge) || !got.UseRemoteOpenSearch(TargetWorkspaceMemory) {
		t.Fatal("expected knowledge and workspace_memory targets to use remote OpenSearch")
	}
	if !got.UseRemotePostgres() {
		t.Fatal("expected UseRemotePostgres true")
	}

	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil || !strings.Contains(string(raw), `"mode": "remote"`) {
		t.Fatalf("saved JSON unexpected: %s (err=%v)", string(raw), err)
	}
}

func TestSecretResolutionPrecedenceAndValidation(t *testing.T) {
	t.Setenv("TEST_PG_DSN", "postgres://env/db")
	t.Setenv("TEST_OS_PW", "env-pw")

	cfg := Config{
		Mode: ModeRemote,
		Remote: RemoteConfig{
			Postgres: PostgresConfig{
				Enabled: true,
				DSNEnv:  "TEST_PG_DSN",
				DSN:     "postgres://inline/db",
			},
			OpenSearch: OpenSearchConfig{
				Enabled:     true,
				Endpoint:    "https://search.example.com",
				PasswordEnv: "TEST_OS_PW",
				Password:    "inline-pw",
				BearerToken: "inline-tok",
			},
		},
	}
	if got := cfg.ResolvePostgresDSN(); got != "postgres://env/db" {
		t.Fatalf("ResolvePostgresDSN = %q, want env value", got)
	}
	if got := cfg.ResolveOpenSearchPassword(); got != "env-pw" {
		t.Fatalf("ResolveOpenSearchPassword = %q, want env value", got)
	}
	if got := cfg.ResolveOpenSearchBearerToken(); got != "inline-tok" {
		t.Fatalf("ResolveOpenSearchBearerToken = %q, want inline value", got)
	}

	bad := Default()
	bad.Mode = "distributed"
	if err := bad.Validate(); err == nil {
		t.Fatal("expected invalid mode error")
	}
	badURL := Default()
	badURL.Remote.OpenSearch.Endpoint = "ftp://example.com"
	if err := badURL.Validate(); err == nil {
		t.Fatal("expected invalid endpoint scheme error")
	}
}
