package workspace

import (
	"errors"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
)

func TestStoreLoadSummary(t *testing.T) {
	root := t.TempDir()
	store, lock, err := CreateSession(root, "Summary test")
	if err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()

	now := time.Now().UTC().Truncate(time.Second)
	want := Session{
		Version:         CurrentVersion,
		ID:              store.SessionID,
		Type:            Interaction,
		RunID:           "run-123",
		Title:           "Heavy session",
		UpdatedAt:       &now,
		HistoryRevision: "rev-1",
		Transcript: []client.Message{
			{Role: client.RoleUser, Content: "hello"},
			{Role: client.RoleAssistant, Content: "world"},
		},
		Context: []client.Message{
			{Role: client.RoleUser, Content: "hello"},
		},
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}

	got, err := store.LoadSummary()
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}
	if got.ID != store.SessionID || got.Title != "Heavy session" || got.RunID != "run-123" ||
		got.Type != Interaction || !got.UpdatedAt.Equal(now) {
		t.Fatalf("unexpected summary: %+v", got)
	}

	missing := Store{Root: root, SessionID: "00000000-0000-4000-8000-000000000000"}
	if _, err := missing.LoadSummary(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing LoadSummary = %v, want ErrNotFound", err)
	}
}
