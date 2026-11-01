package app

import (
	"strings"
	"testing"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

func TestInterruptedGenerationRetainsTextWithoutFeedingItBackToModel(t *testing.T) {
	store, lock, err := workspace.CreateSession(t.TempDir(), "partial output")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	settings := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test"
	m := newModel(t.Context(), settings, nil)
	m.enterChat(value, &fakeClient{})
	m.workspaceStore = &store
	m.waiting = true
	m.streamResponse = "This answer was interrupted."
	updated, _ := m.interruptTurn()
	m = updated.(model)
	for _, message := range m.memory.Messages() {
		if strings.Contains(message.TextContent(), "This answer was interrupted.") {
			t.Fatal("partial text became model context")
		}
	}
	saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range saved.Transcript {
		found = found || message.Name == "q_incomplete_generation" && strings.Contains(message.TextContent(), "This answer was interrupted.")
	}
	if !found {
		t.Fatalf("partial output not persisted: %+v", saved.Transcript)
	}
}
