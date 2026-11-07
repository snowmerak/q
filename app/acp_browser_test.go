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

func TestACPBrowserPreferenceRetriesChangesClearsAndResets(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.UseManagedGateway()
	value.BrowserControl.Enabled = true
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	connection := &fakeACPRemoteConnection{newSessionID: "new-session"}
	remote := &acpRemoteClient{connection: connection, sessionID: "existing-session", browserStore: &store}
	var prompts []string
	connection.prompt = func(_ context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
		prompts = append(prompts, request.Prompt[0].Text.Text)
		if len(prompts) == 1 {
			return acp.PromptResponse{}, errors.New("temporary failure")
		}
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}
	_, _, _ = remote.prompt(t.Context(), "first", nil)
	for range 3 {
		if _, _, err := remote.prompt(t.Context(), "next", nil); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(prompts[0], "q_browser_control") || !strings.Contains(prompts[1], "q_browser_control") || prompts[2] != "next" || prompts[3] != "next" {
		t.Fatalf("retry/dedup: %#v", prompts)
	}
	value.BrowserControl.Preferred = "playwright-cli"
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	if _, _, err := remote.prompt(t.Context(), "changed", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompts[4], `"id":"playwright-cli"`) {
		t.Fatal("changed preference not delivered")
	}
	value.BrowserControl.Enabled = false
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, _, err := remote.prompt(t.Context(), "disabled", nil); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(prompts[5], "no longer active") || prompts[6] != "disabled" {
		t.Fatal("preference was not cleared exactly once")
	}
	value.BrowserControl.Enabled = true
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.newSession(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := remote.prompt(t.Context(), "reset", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompts[7], "q_browser_control") {
		t.Fatal("new session did not receive preference")
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

func TestResumedACPBrowserPreferenceCanBeDisabledOffline(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.UseManagedGateway()
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
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
	remote := &acpRemoteClient{connection: connection, sessionID: "resumed", resumed: true, browserStore: &store, lastBrowserPreference: restored.BrowserPreference}
	var prompts []string
	connection.prompt = func(_ context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
		prompts = append(prompts, request.Prompt[0].Text.Text)
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}
	for range 2 {
		if _, _, err := remote.prompt(t.Context(), "next", nil); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(prompts[0], "no longer active") || prompts[1] != "next" {
		t.Fatalf("offline disable was not delivered exactly once: %#v", prompts)
	}
}
