package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/workspace"
)

func TestResponsesReplayUsesPrivateSessionField(t *testing.T) {
	messages := []client.Message{{Role: client.RoleUser, Content: "question"}, {
		Role: client.RoleAssistant, Content: "answer",
		ResponseModel: "native/model", ResponseOutput: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"private-state"}`), json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"answer"}]}`)},
	}}
	session := workspace.Session{Context: messages, ResponseReplay: collectResponseReplay(messages)}
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var persisted workspace.Session
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mustJSON(t, persisted.Context)), "private-state") {
		t.Fatal("encrypted reasoning leaked into visible messages")
	}
	restored := restoreResponseReplay(persisted.Context, persisted.ResponseReplay)
	if len(restored[1].ResponseOutput) != 2 || restored[1].ResponseModel != "native/model" || !strings.Contains(string(restored[1].ResponseOutput[0]), "private-state") {
		t.Fatalf("replay was not restored: %#v", restored[1])
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestVisibleThoughtHistoryRoundtripKeepsPortablePositionsAndPrivateContext(t *testing.T) {
	base := []client.Message{{Role: client.RoleSystem, Content: "new host instructions"}}
	messages := []client.Message{
		{Role: client.RoleSystem, Content: "old host instructions"},
		{Role: client.RoleUser, Content: "question"},
		{Role: client.RoleAssistant, Content: "answer"},
		{Role: client.RoleUser, Content: "interrupted question"},
	}
	thoughts := []transcriptThought{{Before: 2, Content: "completed visible thought"}, {Before: 4, Content: "partial visible thought"}}
	session := workspace.Session{Transcript: workspaceSessionMessages(messages), Context: workspaceSessionMessages(messages),
		TranscriptThoughts: collectTranscriptThoughts(messages, thoughts)}
	store := workspace.Store{Root: t.TempDir()}
	if err := store.Save(session); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.TranscriptThoughts) != 2 || saved.TranscriptThoughts[0].Before != 1 || saved.TranscriptThoughts[1].Before != 3 {
		t.Fatalf("portable thoughts = %#v", saved.TranscriptThoughts)
	}
	if strings.Contains(string(mustJSON(t, saved.Context)), "visible thought") {
		t.Fatal("display reasoning was inserted into model context")
	}
	restored := restoreTranscriptThoughts(base, saved.Transcript, saved.TranscriptThoughts)
	if len(restored) != 2 || restored[0] != thoughts[0] || restored[1] != thoughts[1] {
		t.Fatalf("restored thoughts = %#v", restored)
	}
}
