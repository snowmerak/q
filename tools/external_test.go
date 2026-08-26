package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/mcpconfig"
)

func TestExternalMCPToolsAreRoleScopedAndCaptured(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "external-test", Version: "1"}, nil)
	type echoInput struct {
		Value string `json:"value" jsonschema:"required"`
	}
	type echoOutput struct {
		Value string `json:"value"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "echo.value", Description: "Echo a value."},
		func(_ context.Context, _ *mcp.CallToolRequest, input echoInput) (*mcp.CallToolResult, echoOutput, error) {
			return nil, echoOutput{Value: input.Value}, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(request *http.Request) *mcp.Server {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("X-ACP-Session") != "session-token" {
			t.Errorf("X-ACP-Session = %q", request.Header.Get("X-ACP-Session"))
		}
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	t.Setenv("TEST_MCP_AUTH", "Bearer test-token")

	runtime, err := NewRuntime(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	value := mcpconfig.Config{
		Version: mcpconfig.CurrentVersion,
		Servers: map[string]mcpconfig.ServerConfig{"docs": {
			Transport: mcpconfig.TransportStreamableHTTP, URL: httpServer.URL,
			Headers:         map[string]string{"Authorization": "TEST_MCP_AUTH"},
			ResolvedHeaders: map[string]string{"X-ACP-Session": "session-token"},
		}},
		Roles: map[string][]string{mcpconfig.RoleDefault: {"docs"}},
	}
	statuses := runtime.ConfigureExternal(context.Background(), t.TempDir(), value)
	if len(statuses) != 1 || statuses[0].Error != "" || statuses[0].Tools != 1 {
		t.Fatalf("statuses = %#v", statuses)
	}
	if hasNamedTool(runtime.ToolsForRole("scout"), "mcp_docs__echo_value") {
		t.Fatal("external tool leaked to unassigned role")
	}
	if !hasNamedTool(runtime.ToolsForRole(mcpconfig.RoleDefault), "mcp_docs__echo_value") {
		t.Fatal("assigned role did not receive external tool")
	}
	result, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "external-1", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "mcp_docs__echo_value", Arguments: `{"value":"hello"}`},
	})
	if err != nil || result.IsError {
		t.Fatalf("Call() = %#v, %v", result, err)
	}
	var receipt struct {
		LoomRef string          `json:"loom_ref"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(result.Content), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.LoomRef == "" || string(receipt.Result) != `{"value":"hello"}` {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func hasNamedTool(available []client.Tool, name string) bool {
	for _, tool := range available {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}

func TestConfigureExternalIsolatesServerFailures(t *testing.T) {
	runtime, err := NewRuntime(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	value := mcpconfig.Config{
		Version: mcpconfig.CurrentVersion,
		Servers: map[string]mcpconfig.ServerConfig{"missing": {Transport: mcpconfig.TransportStdio, Command: "definitely-not-a-q-test-command"}},
		Roles:   map[string][]string{mcpconfig.RoleDefault: {"missing"}},
	}
	statuses := runtime.ConfigureExternal(context.Background(), t.TempDir(), value)
	if len(statuses) != 1 || statuses[0].Error == "" {
		t.Fatalf("statuses = %#v", statuses)
	}
	if len(runtime.ToolsForRole(mcpconfig.RoleDefault)) != len(runtime.Tools()) {
		t.Fatal("failed external server changed builtin tool catalog")
	}
}

func TestExternalScopeKeepsSessionServersOutOfBaseRuntime(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "scope-test", Version: "1"}, nil)
	type echoInput struct {
		Value string `json:"value" jsonschema:"required"`
	}
	type echoOutput struct {
		Value string `json:"value"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo a value."},
		func(_ context.Context, _ *mcp.CallToolRequest, input echoInput) (*mcp.CallToolResult, echoOutput, error) {
			return nil, echoOutput{Value: input.Value}, nil
		})
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer httpServer.Close()

	root := t.TempDir()
	runtime, err := NewRuntime(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	base := mcpconfig.Config{
		Version: mcpconfig.CurrentVersion,
		Servers: map[string]mcpconfig.ServerConfig{"workspace": {
			Transport: mcpconfig.TransportStreamableHTTP, URL: httpServer.URL,
		}},
		Roles: map[string][]string{mcpconfig.RoleDefault: {"workspace"}},
	}
	if statuses := runtime.ConfigureExternal(context.Background(), root, base); len(statuses) != 1 || statuses[0].Error != "" {
		t.Fatalf("base statuses = %#v", statuses)
	}
	session := mcpconfig.Config{
		Version: mcpconfig.CurrentVersion,
		Servers: map[string]mcpconfig.ServerConfig{"acp_session_1": {
			Transport: mcpconfig.TransportStreamableHTTP, URL: httpServer.URL,
		}},
		Roles: map[string][]string{mcpconfig.RoleDefault: {"acp_session_1"}},
	}
	scope, statuses := runtime.NewExternalScope(context.Background(), root, session)
	if len(statuses) != 1 || statuses[0].Error != "" {
		t.Fatalf("scope statuses = %#v", statuses)
	}
	if !hasNamedTool(scope.ToolsForRole(mcpconfig.RoleDefault), "mcp_workspace__echo") ||
		!hasNamedTool(scope.ToolsForRole(mcpconfig.RoleDefault), "mcp_acp_session_1__echo") {
		t.Fatalf("scoped tools = %#v", scope.ToolsForRole(mcpconfig.RoleDefault))
	}
	if hasNamedTool(runtime.ToolsForRole(mcpconfig.RoleDefault), "mcp_acp_session_1__echo") {
		t.Fatal("session MCP tool leaked into the base runtime")
	}
	if _, err := scope.Call(context.Background(), client.ToolCall{
		ID: "scope-1", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "mcp_acp_session_1__echo", Arguments: `{"value":"session"}`},
	}); err != nil {
		t.Fatal(err)
	}
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Call(context.Background(), client.ToolCall{
		ID: "base-1", Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "mcp_workspace__echo", Arguments: `{"value":"workspace"}`},
	}); err != nil {
		t.Fatalf("closing scope disrupted base MCP: %v", err)
	}
}

func TestStdioTransportMapsEnvironmentReferencesAndWorkspace(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SOURCE_TOKEN", "secret-value")
	transport, err := externalTransport(root, "local", mcpconfig.ServerConfig{
		Transport:   mcpconfig.TransportStdio,
		Command:     "server-command",
		Args:        []string{"serve"},
		Env:         map[string]string{"TARGET_TOKEN": "SOURCE_TOKEN"},
		ResolvedEnv: map[string]string{"TARGET_TOKEN": "session-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	commandTransport, ok := transport.(*mcp.CommandTransport)
	if !ok {
		t.Fatalf("transport = %T", transport)
	}
	if filepath.Clean(commandTransport.Command.Dir) != filepath.Clean(root) {
		t.Fatalf("command dir = %q", commandTransport.Command.Dir)
	}
	found := false
	for _, item := range commandTransport.Command.Env {
		if strings.HasPrefix(item, "TARGET_TOKEN=") {
			found = item == "TARGET_TOKEN=session-secret"
		}
	}
	if !found {
		t.Fatal("mapped environment variable was not present")
	}
}
