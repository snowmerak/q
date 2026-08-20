package workspace

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestModelConfigRoundTripAndClear(t *testing.T) {
	store := Store{Root: t.TempDir()}
	missing, err := store.LoadModelConfig()
	if err != nil || missing.Version != ModelConfigVersion || len(missing.Overrides) != 0 {
		t.Fatalf("missing model config = %#v, err = %v", missing, err)
	}
	want := ModelConfig{Overrides: map[string]ModelOverride{
		"default": {Model: "local/qwen"},
		"planner": {Model: "local/planner"},
	}}
	if err := store.SaveModelConfig(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadModelConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != ModelConfigVersion || len(got.Overrides) != 2 ||
		got.Overrides["default"] != want.Overrides["default"] || got.Overrides["planner"] != want.Overrides["planner"] {
		t.Fatalf("model config = %#v", got)
	}
	if err := store.ClearModelConfig(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.ModelPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("model config survived clear: %v", err)
	}
}

func TestModelConfigValidation(t *testing.T) {
	store := Store{Root: t.TempDir()}
	for _, value := range []ModelConfig{
		{Overrides: map[string]ModelOverride{"": {Model: "valid"}}},
		{Overrides: map[string]ModelOverride{"default": {Model: " spaced "}}},
		{Overrides: map[string]ModelOverride{"thinker": {Model: "workspace-thinker"}}},
		{Overrides: map[string]ModelOverride{"embedding": {Model: "workspace-embedding"}}},
	} {
		if err := store.SaveModelConfig(value); err == nil {
			t.Fatalf("SaveModelConfig(%#v) succeeded", value)
		}
	}
}

func TestLoadModelConfigRejectsUnknownFields(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ModelPath(), []byte(`{"version":2,"overrides":{"default":{"model":"test"}},"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadModelConfig(); err == nil {
		t.Fatal("LoadModelConfig accepted an unknown field")
	}
}

func TestLoadModelConfigMigratesVersionOneWithoutContextWindow(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":1,"overrides":{"default":{"model":"local/qwen","context_window":8192},"planner":{"model":"local/planner"}}}`
	if err := os.WriteFile(store.ModelPath(), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadModelConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != ModelConfigVersion || got.Overrides["default"].Model != "local/qwen" || got.Overrides["planner"].Model != "local/planner" {
		t.Fatalf("migrated model config = %#v", got)
	}
	if err := store.SaveModelConfig(got); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(store.ModelPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == legacy || containsJSONField(body, "context_window") {
		t.Fatalf("legacy context window survived migration: %s", body)
	}
}

func TestLoadCurrentModelConfigRejectsContextWindow(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	value := `{"version":2,"overrides":{"default":{"model":"local/qwen","context_window":8192}}}`
	if err := os.WriteFile(store.ModelPath(), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadModelConfig(); err == nil {
		t.Fatal("LoadModelConfig accepted context_window in the current schema")
	}
}

func containsJSONField(body []byte, field string) bool {
	return strings.Contains(string(body), `"`+field+`"`)
}
