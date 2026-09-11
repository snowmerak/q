package app

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
	qtools "github.com/snowmerak/q/tools"
)

func TestExecuteACPExternalWebTesterCorrectsOnceAndDisposesSession(t *testing.T) {
	connection := &fakeACPRemoteConnection{}
	remote := &acpRemoteClient{
		connection: connection, sessionID: "web-session", display: "Browser", agent: "browser",
		capabilities: acp.AgentCapabilities{SessionCapabilities: acp.SessionCapabilities{
			Delete: &acp.SessionDeleteCapabilities{}, Close: &acp.SessionCloseCapabilities{},
		}},
	}
	prompts := 0
	connection.prompt = func(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
		prompts++
		if request.SessionId != "web-session" || request.Prompt[0].Text == nil {
			t.Fatalf("prompt = %#v", request)
		}
		text := request.Prompt[0].Text.Text
		response := "not-json"
		if prompts == 1 {
			if !strings.Contains(text, "verify login") || !strings.Contains(text, "Operate autonomously") {
				t.Fatalf("initial prompt = %q", text)
			}
			permission, err := remote.RequestPermission(ctx, acp.RequestPermissionRequest{
				SessionId: "web-session",
				Options: []acp.PermissionOption{
					{OptionId: "always", Name: "Always", Kind: acp.PermissionOptionKindAllowAlways},
					{OptionId: "once", Name: "Once", Kind: acp.PermissionOptionKindAllowOnce},
				},
			})
			if err != nil || permission.Outcome.Selected == nil || permission.Outcome.Selected.OptionId != "once" {
				t.Fatalf("automatic permission = %#v, %v", permission, err)
			}
		} else {
			if !strings.Contains(text, "did not satisfy") {
				t.Fatalf("correction prompt = %q", text)
			}
			response = `{"outcome":"failed","summary":"login cookie was not stored","findings":["cookie missing"],"verification":["submitted login form"]}`
		}
		if err := remote.SessionUpdate(ctx, acp.SessionNotification{
			SessionId: "web-session", Update: acp.SessionUpdate{
				AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{Content: acp.TextBlock(response)},
			},
		}); err != nil {
			return acp.PromptResponse{}, err
		}
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}
	result, err := executeACPExternalWebTester(t.Context(), remote, "browser-main", subagent.ExternalWebTesterInput{
		Request: "verify login", CompletionCriteria: []string{"report product failure"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prompts != 2 || result.Agent != "browser-main" || result.Outcome != "failed" ||
		result.Summary != "login cookie was not stored" {
		t.Fatalf("prompts=%d result=%#v", prompts, result)
	}
	if remote.permissions != acpPermissionAutomatic ||
		!slices.Equal(connection.deleted, []acp.SessionId{"web-session"}) || remote.sessionID != "" {
		t.Fatalf("permissions=%v deleted=%v session=%q", remote.permissions, connection.deleted, remote.sessionID)
	}
}

func TestConfiguredExternalWebTesterRequiresEnabledAssignment(t *testing.T) {
	value := config.Default()
	if _, configured := configuredExternalWebTesterInvocation(value, t.TempDir()); configured {
		t.Fatal("unassigned web tester was configured")
	}
	value.Agents.Connections = map[string]config.AgentConnectionConfig{"browser": {Preset: "codex", Disabled: true}}
	value.Agents.Roles = map[string]config.AgentConfig{config.AgentRoleExternalWebTester: {Agent: "browser"}}
	if _, configured := configuredExternalWebTesterInvocation(value, t.TempDir()); configured {
		t.Fatal("disabled web tester was configured")
	}
	connection := value.Agents.Connections["browser"]
	connection.Disabled = false
	value.Agents.Connections["browser"] = connection
	if _, configured := configuredExternalWebTesterInvocation(value, t.TempDir()); !configured {
		t.Fatal("enabled web tester was not configured")
	}
}

func TestExternalWebTesterACPIntegration(t *testing.T) {
	if strings.TrimSpace(os.Getenv("Q_TEST_ACP_WEB_TESTER")) == "" {
		t.Skip("set Q_TEST_ACP_WEB_TESTER=1 and Q_TEST_ACP_PRESET=codex or grok to run a real external Web Tester turn")
	}
	preset := strings.TrimSpace(os.Getenv("Q_TEST_ACP_PRESET"))
	if preset == "" {
		t.Fatal("Q_TEST_ACP_PRESET must be codex or grok")
	}
	root := t.TempDir()
	runtime, err := qtools.NewRuntime(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	value := config.Default()
	value.Agents.Connections = map[string]config.AgentConnectionConfig{preset: {Preset: preset}}
	value.Agents.Roles = map[string]config.AgentConfig{
		config.AgentRoleExternalWebTester: {Agent: preset},
	}
	configured, err := configuredAgentToolRuntime(runtime, "default", value, root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 16*time.Minute)
	defer cancel()
	parent := &fakeClient{}
	events := make(chan agentEvent, 8)
	streamAgentWebTester(ctx, configured, "Inspect the workspace and verify it is accessible. Do not edit files.", "real-web-test", agentSearchParent{
		client: parent, tools: configured, model: "test-model",
	}, events)
	for event := range events {
		if event.err != nil {
			t.Fatal(event.err)
		}
	}
	if len(parent.requests) != 1 {
		t.Fatalf("parent requests = %#v", parent.requests)
	}
	last := parent.requests[0].Messages[len(parent.requests[0].Messages)-1]
	if last.Role != "tool" || !strings.Contains(last.Content, "loom_ref") {
		t.Fatalf("captured real result = %#v", last)
	}
}
