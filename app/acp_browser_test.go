package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
	"github.com/snowmerak/q/workspace"
)

func TestNewACPConversationHasNoBrowserPreference(t *testing.T) {
	connection := &fakeACPRemoteConnection{newSessionID: "new-session"}
	remote := &acpRemoteClient{connection: connection, lastBrowserPreference: "previous session only"}
	if _, err := remote.newSession(t.Context()); err != nil {
		t.Fatal(err)
	}
	connection.prompt = func(_ context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
		if request.Prompt[0].Text.Text != "current request" {
			t.Fatalf("unexpected browser preference in new ACP prompt: %s", request.Prompt[0].Text.Text)
		}
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}
	if _, _, err := remote.prompt(t.Context(), "current request", nil); err != nil {
		t.Fatal(err)
	}
}

func TestExistingSessionReadsLatestBrowserPreferenceWithoutChangingModel(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.UseManagedGateway()
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	m := model{store: store, config: value, conversationID: "durable-thread"}
	if m.browserPreference() != "" {
		t.Fatal("disabled preference was injected")
	}
	value.BrowserControl.Enabled = true
	value.BrowserControl.Preferred = "playwright-cli"
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.browserPreference(), `"id":"playwright-cli"`) || m.config.BrowserControl.Enabled || m.conversationID != "durable-thread" {
		t.Fatal("existing session did not read latest browser settings or changed its identity")
	}
}

func TestResumedACPWithdrawsPreviousBrowserPreference(t *testing.T) {
	connection := &fakeACPRemoteConnection{}
	conversation := workspace.Store{Root: t.TempDir(), SessionID: "test-session"}
	if err := os.MkdirAll(conversation.SessionDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := &acpRemoteClient{sessionID: "resumed", sessionCheckpoint: &acpSessionCheckpoint{Agent: "fixture"}, lastBrowserPreference: "Previously configured browser CLI"}
	if err := saveACPSessionCheckpoint(conversation, previous, []client.Message{{Role: client.RoleAssistant, Content: "done"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(acpSessionPath(conversation))
	if err != nil {
		t.Fatal(err)
	}
	var restored acpSessionCheckpoint
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.BrowserPreference != previous.lastBrowserPreference {
		t.Fatal("checkpoint lost browser preference")
	}
	remote := &acpRemoteClient{connection: connection, sessionID: "resumed", resumed: true, lastBrowserPreference: restored.BrowserPreference}
	var prompts []string
	connection.prompt = func(_ context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
		prompts = append(prompts, request.Prompt[0].Text.Text)
		if len(prompts) == 1 {
			return acp.PromptResponse{}, errors.New("temporary failure")
		}
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}
	if _, _, err := remote.prompt(t.Context(), "next", nil); err == nil {
		t.Fatal("expected prompt failure")
	}
	for range 2 {
		if _, _, err := remote.prompt(t.Context(), "next", nil); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(prompts[0], "no longer active") || prompts[1] != prompts[0] || prompts[2] != "next" {
		t.Fatalf("withdrawal was not retried once or kept appearing after success: %#v", prompts)
	}
}
