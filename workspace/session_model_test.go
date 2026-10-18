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
	if err := store.SaveSessionModelConfig(SessionModelConfig{Model: "provider/model"}); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ResetSession(store.Root, store.SessionID, "test clear"); err != nil {
		t.Fatal(err)
	}
	value, err := store.LoadSessionModelConfig()
	if err != nil || value.Model != "provider/model" {
		t.Fatalf("model after clear = %+v, %v", value, err)
	}
	if err := store.ClearSession(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.SessionDir()); !os.IsNotExist(err) {
		t.Fatalf("deleted session directory remains: %v", err)
	}
}
