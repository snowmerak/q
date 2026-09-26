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
