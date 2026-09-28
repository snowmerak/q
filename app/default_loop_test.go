package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
)

func TestDefaultLoopOffersDirectAndDelegationTools(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.config = value
	m.client = &fakeClient{models: []client.Model{{ID: "test-model"}}}
	base := &fakeAgentTools{tools: []client.Tool{
		{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "read_file"}},
		{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "write_file"}},
	}}
	m.toolRuntime = base
	root := t.TempDir()
	m.workspaceStore = &workspace.Store{Root: root}

	runtime, err := m.configuredDelegationRuntime(base, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"read_file", "write_file", subagent.DelegateListToolName, subagent.DelegateToolName} {
		if !toolAvailable(runtime, name) {
			t.Fatalf("default loop tool %q is unavailable", name)
		}
	}
	if _, err := runtime.Call(context.Background(), client.ToolCall{Function: client.FunctionCall{Name: "write_file"}}); err != nil {
		t.Fatal(err)
	}
	if len(base.calls) != 1 || base.calls[0].Function.Name != "write_file" {
		t.Fatalf("direct tool call did not reach base runtime: %#v", base.calls)
	}
}

func TestLegacyDelegationModeMessagesAreRemoved(t *testing.T) {
	messages := []client.Message{
		{Role: client.RoleSystem, Name: legacyDelegationPromptName, Content: "coordinate only"},
		{Role: client.RoleDeveloper, Name: legacyDelegationPolicyName, Content: "no direct tools"},
		{Role: client.RoleUser, Content: "keep this"},
	}

	filtered := withoutLegacyDelegationModeMessages(messages)
	if len(filtered) != 1 || filtered[0].Role != client.RoleUser || filtered[0].TextContent() != "keep this" {
		t.Fatalf("filtered messages = %#v", filtered)
	}
}

func TestRetiredLoopModeCommandDoesNotStartATurn(t *testing.T) {
	configured := &fakeClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(config.Default(), configured)
	m.input.SetValue("/mode delegation")

	updated, _ := m.submitChat()
	m = updated.(model)
	if m.waiting || len(configured.requests) != 0 || m.status != "Loop modes were removed. Ordinary chat can use direct tools and delegate work." {
		t.Fatalf("retired mode command started a turn: waiting=%v requests=%d status=%q", m.waiting, len(configured.requests), m.status)
	}
}

func TestRestoringLegacyDelegationMessagesPreservesResponseReplay(t *testing.T) {
	root := t.TempDir()
	store := workspace.Store{Root: root}
	transcript := []client.Message{
		{Role: client.RoleSystem, Name: legacyDelegationPromptName, Content: "coordinate only"},
		{Role: client.RoleDeveloper, Name: legacyDelegationPolicyName, Content: "no direct tools"},
		{Role: client.RoleUser, Content: "question"},
		{Role: client.RoleAssistant, Content: "answer"},
	}
	if err := store.Save(workspace.Session{
		RunID: "run-test", Transcript: transcript, Context: transcript,
		ResponseReplay: []workspace.ResponseReplayItem{{
			Index: 3, Model: "test-model",
			Output: []json.RawMessage{json.RawMessage(`{"type":"message"}`)},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	value := config.Default()
	value.Provider.Model = "test-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &store
	m.enterChat(value, &fakeClient{})

	for _, messages := range [][]client.Message{m.messages, m.memory.Messages()} {
		for _, message := range messages {
			if message.Name == legacyDelegationPromptName || message.Name == legacyDelegationPolicyName {
				t.Fatalf("legacy delegation message restored: %#v", message)
			}
		}
	}
	foundReplay := false
	for _, message := range m.memory.Messages() {
		if message.Role == client.RoleAssistant && message.TextContent() == "answer" {
			foundReplay = len(message.ResponseOutput) == 1
		}
	}
	if !foundReplay {
		t.Fatalf("response replay was lost: %#v", m.memory.Messages())
	}
}
