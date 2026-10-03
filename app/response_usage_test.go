package app

import (
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/workspace"
)

func TestResponseUsagePreservesUnreportedAndZeroCache(t *testing.T) {
	if got := responseTokenUsage(client.Usage{}); got != nil {
		t.Fatalf("unreported usage = %#v", got)
	}
	for _, reported := range []bool{false, true} {
		usage := client.Usage{PromptTokens: 2000, CompletionTokens: 90}
		if reported {
			usage.PromptDetails = &client.TokenDetails{}
		}
		got := responseTokenUsage(usage)
		if got.InputTokens != 2000 || got.OutputTokens != 90 || (got.CachedTokens != nil) != reported {
			t.Fatalf("cache reported=%v: %#v", reported, got)
		}
		if got.CachedTokens != nil && *got.CachedTokens != 0 {
			t.Fatalf("reported zero cache = %d", *got.CachedTokens)
		}
	}
}

func TestResponseUsageSurvivesSaveRestoreAndClear(t *testing.T) {
	store := workspace.Store{Root: t.TempDir()}
	state := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	state.workspaceStore = &store
	state.messages = []client.Message{
		{Role: client.RoleSystem, Content: "host instructions"},
		{Role: client.RoleUser, Content: "old turn"},
		{Role: client.RoleAssistant, Content: "old answer without usage"},
		{Role: client.RoleUser, Content: "new turn"},
		{Role: client.RoleAssistant, Content: "new answer"},
	}
	state.memory = memory.New(memory.Policy{}, state.messages)
	state.recordResponseUsage(client.Usage{PromptTokens: 2048, CompletionTokens: 96, PromptDetails: &client.TokenDetails{CachedTokens: 1536}})
	if err := state.saveWorkspaceSession(); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.ResponseUsage) != 1 || saved.ResponseUsage[0].AssistantIndex != 1 || *saved.ResponseUsage[0].CachedTokens != 1536 {
		t.Fatalf("saved usage = %#v", saved.ResponseUsage)
	}
	restored := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	restored.workspaceStore = &store
	restored.enterChat(config.Default(), &fakeClient{})
	if len(restored.responseUsage) != 1 || restored.responseUsage[0].AssistantIndex != 1 {
		t.Fatalf("restored usage = %#v", restored.responseUsage)
	}
	restored.resetConversationState()
	if len(restored.responseUsage) != 0 {
		t.Fatalf("cleared conversation retained usage: %#v", restored.responseUsage)
	}
}
