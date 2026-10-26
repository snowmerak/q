package workspace

import (
	"os"
	"testing"
)

func TestSessionModelSurvivesResetAndIsDeletedWithSession(t *testing.T) {
	store, lock, err := CreateSession(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessionModelConfig(SessionModelConfig{Model: "provider/model", ReasoningEffort: "high"}); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ResetSession(store.Root, store.SessionID, "test clear"); err != nil {
		t.Fatal(err)
	}
	value, err := store.LoadSessionModelConfig()
	if err != nil || value.Model != "provider/model" || value.ReasoningEffort != "high" {
		t.Fatalf("model after clear = %+v, %v", value, err)
	}
	if err := store.ClearSession(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.SessionDir()); !os.IsNotExist(err) {
		t.Fatalf("deleted session directory remains: %v", err)
	}
}

func TestSessionReasoningConfigValidationAndLegacyModel(t *testing.T) {
	store, lock, err := CreateSession(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := os.WriteFile(store.SessionModelPath(), []byte(`{"model":"provider/model"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := store.LoadSessionModelConfig()
	if err != nil || value.ReasoningEffort != "" {
		t.Fatalf("legacy=%+v %v", value, err)
	}
	for _, effort := range []string{" high", "high\nlow", string(make([]byte, 65))} {
		if err := (SessionModelConfig{ReasoningEffort: effort}).Validate(); err == nil {
			t.Fatalf("invalid effort accepted: %q", effort)
		}
	}
}
