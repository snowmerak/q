package app

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
	qtools "github.com/snowmerak/q/tools"
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
	if _, configured := configuredExternalSearchInvocation(value, t.TempDir()); configured {
		t.Fatal("unassigned search role was enabled")
	}
	value.Agents.Connections = map[string]config.AgentConnectionConfig{"codex": {Preset: "codex", Disabled: true}}
	value.Agents.Roles = map[string]config.AgentConfig{config.AgentRoleSearch: {Agent: "codex"}}
	if _, configured := configuredExternalSearchInvocation(value, t.TempDir()); configured {
		t.Fatal("disabled search connection was enabled")
	}
	connection := value.Agents.Connections["codex"]
	connection.Disabled = false
	value.Agents.Connections["codex"] = connection
	if _, configured := configuredExternalSearchInvocation(value, t.TempDir()); !configured {
		t.Fatal("enabled search assignment was not configured")
	}
}

func TestExternalSearchToolIsExposedOnlyToParentRoles(t *testing.T) {
	value := config.Default()
	value.Agents.Connections = map[string]config.AgentConnectionConfig{"codex": {Preset: "codex"}}
	value.Agents.Roles = map[string]config.AgentConfig{config.AgentRoleSearch: {Agent: "codex"}}
	for _, role := range []string{mcpconfig.RoleDefault, config.AgentRoleGriller, config.AgentRolePlanner, config.AgentRoleAdvisor} {
		runtime, err := configuredAgentToolRuntime(&fakeAgentTools{}, role, value, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if !toolAvailable(runtime, subagent.ExternalSearchToolName) {
			t.Fatalf("external_search was not exposed to %q", role)
		}
	}
	for _, role := range []string{config.AgentRoleSearch, config.AgentRoleScout, config.AgentRoleCoder} {
		runtime, err := configuredAgentToolRuntime(&fakeAgentTools{}, role, value, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if toolAvailable(runtime, subagent.ExternalSearchToolName) {
			t.Fatalf("external_search was exposed recursively to %q", role)
		}
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
	parentClient := &fakeClient{}
	agent, workspaceStore, connection := testACPAgent(t, parentClient, &fakeAgentTools{})
	toolRuntime, err := qtools.NewRuntime(t.Context(), workspaceStore.Root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = toolRuntime.Close() })
	agent.state.toolRuntime = toolRuntime
	value := agent.state.activeConfig()
	value.Agents.Connections = map[string]config.AgentConnectionConfig{preset: {Preset: preset}}
	value.Agents.Roles = map[string]config.AgentConfig{config.AgentRoleSearch: {Agent: preset}}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	agent.state.config = value

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
	if response.StopReason != acp.StopReasonEndTurn || strings.TrimSpace(output) == "" {
		t.Fatalf("real /agent:search returned stop reason %q without a parent response", response.StopReason)
	}
	if len(parentClient.requests) != 1 ||
		!strings.Contains(parentClient.requests[0].Messages[len(parentClient.requests[0].Messages)-1].Content, "http") ||
		!strings.Contains(parentClient.requests[0].Messages[len(parentClient.requests[0].Messages)-1].Content, "loom_ref") {
		t.Fatalf("real /agent:search did not return sourced evidence to the parent: %#v", parentClient.requests)
	}
}
