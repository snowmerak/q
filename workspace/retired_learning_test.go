package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestLegacyPendingLearningIsDiscardedOnRestoreAndSave(t *testing.T) {
	store, lock, err := CreateSession(t.TempDir(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lock.Close(); err != nil {
			t.Error(err)
		}
	}()
	legacy := Session{Version: CurrentVersion, ID: store.SessionID, RunID: "legacy-run", Transcript: []client.Message{{Role: client.RoleUser, Content: "Preserve this conversation"}}, Learning: json.RawMessage(`{"pending":true,"request":{"content":"old automatic learning"}}`)}
	body, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), body, 0o600); err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(store.SessionDir(), ThinkerCheckpointFileName)
	if err := os.WriteFile(checkpoint, []byte(`{"pending":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || len(loaded.Learning) != 0 || len(loaded.Transcript) != 1 {
		t.Fatalf("legacy restore: %+v %v", loaded, err)
	}
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(store.Path())
	if err != nil || strings.Contains(string(saved), `"learning"`) {
		t.Fatalf("retired metadata survived save: %s %v", saved, err)
	}
	if err := store.ClearSession(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(checkpoint); !os.IsNotExist(err) {
		t.Fatalf("legacy checkpoint survived cleanup: %v", err)
	}
}
