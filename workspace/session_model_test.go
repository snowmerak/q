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

func TestSessionACPSelectionSurvivesReset(t *testing.T) {
	store, lock, err := CreateSession(t.TempDir(), "ACP selection")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessionModelConfig(SessionModelConfig{Agent: "local-acp", AgentModel: "external/model", AgentReasoningEffort: "high"}); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ResetSession(store.Root, store.SessionID, "clear"); err != nil {
		t.Fatal(err)
	}
	selected, err := store.LoadSessionModelConfig()
	if err != nil || selected.Agent != "local-acp" || selected.AgentModel != "external/model" || selected.AgentReasoningEffort != "high" || selected.Model != "" {
		t.Fatalf("ACP selection after clear = %+v, %v", selected, err)
	}
	for _, invalid := range []SessionModelConfig{
		{Agent: "local-acp", Model: "provider/model"}, {Agent: "local-acp", ReasoningEffort: "high"}, {Agent: " local-acp"}, {Agent: "other/agent"},
		{AgentModel: "model"}, {Agent: "local-acp", AgentModel: " model"},
		{AgentReasoningEffort: "high"}, {Agent: "local-acp", AgentReasoningEffort: " high"},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid ACP selection accepted: %+v", invalid)
		}
	}
}
