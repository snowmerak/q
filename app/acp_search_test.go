package app

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

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

func TestDisposeACPRemoteFallsBackToCloseWhenDeleteFails(t *testing.T) {
	connection := &fakeACPRemoteConnection{deleteErr: errors.New("empty session cannot be archived")}
	remote := &acpRemoteClient{
		connection: connection, sessionID: "empty-session",
		capabilities: acp.AgentCapabilities{SessionCapabilities: acp.SessionCapabilities{
			Delete: &acp.SessionDeleteCapabilities{}, Close: &acp.SessionCloseCapabilities{},
		}},
	}
	if err := disposeACPRemote(remote); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(connection.deleted, []acp.SessionId{"empty-session"}) ||
		!slices.Equal(connection.closed, []acp.SessionId{"empty-session"}) || remote.sessionID != "" {
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

func TestACPConnectionProbeIntegration(t *testing.T) {
	preset := strings.TrimSpace(os.Getenv("Q_TEST_ACP_PRESET"))
	if preset == "" {
		t.Skip("set Q_TEST_ACP_PRESET=codex or grok to probe an installed ACP agent")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if err := probeACPAgentConnection(ctx, t.TempDir(), preset, config.AgentConnectionConfig{Preset: preset}); err != nil {
		t.Fatal(err)
	}
}

func TestACPAgentExplicitSearchIntegration(t *testing.T) {
	if strings.TrimSpace(os.Getenv("Q_TEST_ACP_SEARCH")) == "" {
		t.Skip("set Q_TEST_ACP_SEARCH=1 and Q_TEST_ACP_PRESET=codex or grok to run a real /agent:search turn")
	}
	preset := strings.TrimSpace(os.Getenv("Q_TEST_ACP_PRESET"))
	if preset == "" {
		t.Fatal("Q_TEST_ACP_PRESET must be codex or grok")
	}
	agent, workspaceStore, connection := testACPAgent(t, &fakeClient{}, &fakeAgentTools{})
	value := agent.state.activeConfig()
	value.Agents.Connections = map[string]config.AgentConnectionConfig{preset: {Preset: preset}}
	value.Agents.Roles = map[string]config.AgentConfig{config.AgentRoleSearch: {Agent: preset}}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	agent.state.config = value
	agent.externalSearch = configuredExternalSearch(value, workspaceStore.Root)

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	response, err := agent.Prompt(ctx, acp.PromptRequest{
		SessionId: sessionID,
		Prompt: []acp.ContentBlock{acp.TextBlock(
			"/agent:search Find the official Agent Client Protocol overview and return at least one direct official URL.",
		)},
	})
	if err != nil {
		t.Fatal(err)
	}
	var output string
	for _, notification := range connection.snapshot() {
		if update := notification.Update.AgentMessageChunk; update != nil && update.Content.Text != nil {
			output += update.Content.Text.Text
		}
	}
	if response.StopReason != acp.StopReasonEndTurn || !strings.Contains(output, "http") {
		t.Fatalf("real /agent:search returned stop reason %q without a sourced report", response.StopReason)
	}
}
