package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	store := Store{Dir: filepath.Join(t.TempDir(), ".q")}
	want := Default()
	want.Provider.Model = "test-model"
	want.Provider.APIKey = "secret"
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("loaded config = %#v; want %#v", got, want)
	}
	want.Provider.Model = "second-model"
	if err := store.Save(want); err != nil {
		t.Fatalf("replace existing config: %v", err)
	}
	got, err = store.Load()
	if err != nil || got.Provider.Model != "second-model" {
		t.Fatalf("reloaded config = %#v, err = %v", got, err)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config permissions are too broad: %o", info.Mode().Perm())
	}
}

func TestStoreNotFound(t *testing.T) {
	_, err := (Store{Dir: t.TempDir()}).Load()
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	body := "version: 1\nprovider:\n  type: openai-compatible\n  base_url: https://example.com/v1\n  model: test\n  surprise: true\n"
	if err := os.WriteFile(store.Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.Load()
	if err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveAPIKeyPrecedence(t *testing.T) {
	t.Setenv("TEST_Q_API_KEY", "from-env")
	provider := ProviderConfig{APIKeyEnv: "TEST_Q_API_KEY"}
	if got := provider.ResolveAPIKey(); got != "from-env" {
		t.Fatalf("key = %q", got)
	}
	provider.APIKey = "inline"
	if got := provider.ResolveAPIKey(); got != "inline" {
		t.Fatalf("key = %q", got)
	}
}

func TestEffectiveContextUsesDefaultsAndExplicitWindow(t *testing.T) {
	value := Config{Provider: ProviderConfig{ContextWindow: 123456}}
	effective := value.EffectiveContext()
	if effective.TriggerRatio != .78 || effective.TargetRatio != .22 || effective.RecentRatio != .07 {
		t.Fatalf("effective context = %#v", effective)
	}
	if got := value.EffectiveContextWindow(); got != 123456 {
		t.Fatalf("model context window = %d", got)
	}
	value.Context.Window = 654321
	if got := value.EffectiveContextWindow(); got != 654321 {
		t.Fatalf("explicit context window = %d", got)
	}
}
