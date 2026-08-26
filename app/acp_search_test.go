package app

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
)

func TestExecuteACPExternalSearchPromptsAndDeletesSession(t *testing.T) {
	connection := &fakeACPRemoteConnection{}
	remote := &acpRemoteClient{
		connection: connection, sessionID: "search-session", display: "Codex", agent: "codex",
		capabilities: acp.AgentCapabilities{SessionCapabilities: acp.SessionCapabilities{
			Delete: &acp.SessionDeleteCapabilities{}, Close: &acp.SessionCloseCapabilities{},
		}},
	}
	connection.prompt = func(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
		if request.SessionId != "search-session" || request.Prompt[0].Text == nil ||
			!strings.Contains(request.Prompt[0].Text.Text, "current release policy") ||
			!strings.Contains(request.Prompt[0].Text.Text, "Do not edit files") {
			t.Fatalf("prompt = %#v", request)
		}
		if err := remote.SessionUpdate(ctx, acp.SessionNotification{
			SessionId: "search-session", Update: acp.SessionUpdate{
				AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{Content: acp.TextBlock(
					"Confirmed by https://example.com/docs and https://example.com/release.",
				)},
			},
		}); err != nil {
			return acp.PromptResponse{}, err
		}
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}

	result, err := executeACPExternalSearch(t.Context(), remote, "codex-main", subagent.ExternalSearchInput{
		Query: "current release policy", CompletionCriteria: []string{"cite primary sources"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Agent != "codex-main" || !strings.Contains(result.Summary, "Confirmed") ||
		!slices.Equal(result.Sources, []string{"https://example.com/docs", "https://example.com/release"}) {
		t.Fatalf("result = %#v", result)
	}
	if !slices.Equal(connection.deleted, []acp.SessionId{"search-session"}) || len(connection.closed) != 0 || remote.sessionID != "" {
		t.Fatalf("deleted=%#v closed=%#v session=%q", connection.deleted, connection.closed, remote.sessionID)
	}
}

func TestConfiguredExternalSearchRequiresEnabledSearchAssignment(t *testing.T) {
	value := config.Default()
	if configuredExternalSearch(value, t.TempDir()) != nil {
		t.Fatal("unassigned search role was enabled")
	}
	value.Agents.Connections = map[string]config.AgentConnectionConfig{"codex": {Preset: "codex", Disabled: true}}
	value.Agents.Roles = map[string]config.AgentConfig{config.AgentRoleSearch: {Agent: "codex"}}
	if configuredExternalSearch(value, t.TempDir()) != nil {
		t.Fatal("disabled search connection was enabled")
	}
	connection := value.Agents.Connections["codex"]
	connection.Disabled = false
	value.Agents.Connections["codex"] = connection
	if configuredExternalSearch(value, t.TempDir()) == nil {
		t.Fatal("enabled search assignment was not configured")
	}
}
