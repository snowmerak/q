package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestProviderSessionMappingRoundTrip(t *testing.T) {
	s := Session{Name: "work", PWD: filepath.Join("root", "work")}
	s.SetProviderSessionID("Codex", "thread-123")
	if got := s.ProviderSessionID("codex"); got != "thread-123" {
		t.Fatalf("ProviderSessionID() = %q", got)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var restored Session
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	if got := restored.ProviderSessionID("CODEX"); got != "thread-123" {
		t.Fatalf("restored ProviderSessionID() = %q", got)
	}
}

func TestOldSessionJSONRemainsCompatible(t *testing.T) {
	var s Session
	if err := json.Unmarshal([]byte(`{"name":"old","messages":[]}`), &s); err != nil {
		t.Fatal(err)
	}
	if got := s.ProviderSessionID("codex"); got != "" {
		t.Fatalf("unexpected provider ID %q", got)
	}
}

func TestProviderSessionMappingIsScopedToWorkingDirectory(t *testing.T) {
	s := Session{Name: "work", PWD: filepath.Join("root", "one")}
	s.SetProviderSessionID("claude", "session-one")
	if got := s.ProviderSessionID("claude"); got != "session-one" {
		t.Fatalf("got %q", got)
	}
	s.PWD = filepath.Join("root", "two")
	if got := s.ProviderSessionID("claude"); got != "" {
		t.Fatalf("cross-directory session = %q", got)
	}
}
