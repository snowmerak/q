package workspace

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestSessionUsesPortableMessageFormatAndPreservesReplay(t *testing.T) {
	store := Store{Root: t.TempDir()}
	user := client.Message{Role: client.RoleUser, ContentParts: []client.MessageContentPart{
		{"type": "text", "text": "Describe this."},
		{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AA==", "detail": "low"}},
	}}
	assistant := client.Message{Role: client.RoleAssistant, Content: "Calling lookup", Phase: "commentary", ToolCalls: []client.ToolCall{{
		ID: "call_1", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "lookup", Arguments: `{"q":1}`},
	}}}
	tool := client.Message{Role: client.RoleTool, ToolCallID: "call_1", Content: "result"}
	replay := []ResponseReplayItem{{Index: 1, Model: "openai/gpt", Output: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)}}}
	if err := store.Save(Session{Transcript: []client.Message{user, assistant, tool}, Context: []client.Message{user, assistant, tool}, ResponseReplay: replay}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"version": 2`) || !strings.Contains(string(body), `"kind": "image"`) || !strings.Contains(string(body), `"phase": "commentary"`) || strings.Contains(string(body), `"image_url"`) {
		t.Fatalf("session file is not portable: %s", body)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Context) != 3 || loaded.Context[0].ContentParts[1]["type"] != "image_url" || loaded.Context[1].Phase != "commentary" || loaded.Context[1].ToolCalls[0].ID != "call_1" || loaded.Context[2].ToolCallID != "call_1" || len(loaded.ResponseReplay) != 1 {
		t.Fatalf("restored session = %#v", loaded)
	}
}

func TestSessionMigratesChatMessageVersionOneOnSave(t *testing.T) {
	const previous = `{"version":1,"transcript":[{"role":"user","content":"hello"},{"role":"assistant","content":"answer","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}],"context":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],"response_replay":[{"index":1,"model":"openai/gpt","output":[{"type":"reasoning","encrypted_content":"opaque"}]}],"learning":{}}`
	store := Store{Root: t.TempDir()}
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != CurrentVersion || loaded.Transcript[1].ToolCalls[0].ID != "call_1" || len(loaded.Context[0].ContentParts) != 2 || len(loaded.ResponseReplay) != 1 {
		t.Fatalf("legacy session = %#v", loaded)
	}
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"version": 2`) || !strings.Contains(string(body), `"kind": "image"`) {
		t.Fatalf("migrated session = %s", body)
	}
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
}
