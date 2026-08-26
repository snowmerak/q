package app

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/mcpconfig"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
)

func TestMCPSettingsAssignServerToRoleAndSave(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	m := newModel(context.Background(), store, nil)
	m.config = config.Default()
	workspaceStore := workspace.Store{Root: t.TempDir()}
	m.workspaceStore = &workspaceStore
	updated, _ := m.enterMCP()
	m = updated.(model)
	m.mcpDraft.Servers["docs"] = mcpconfig.ServerConfig{Transport: mcpconfig.TransportStdio, Command: "docs-server"}
	updated, command := m.toggleMCPAssignment()
	m = updated.(model)
	if !containsMCPID(m.mcpDraft.Roles[mcpconfig.RoleDefault], "docs") {
		t.Fatal("server was not assigned to selected role")
	}
	if command == nil {
		t.Fatal("automatic save command is nil")
	}
	message := command()
	updated, _ = m.Update(message)
	m = updated.(model)
	loaded, err := (mcpconfig.Store{Dir: store.Dir}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if !containsMCPID(loaded.Roles[mcpconfig.RoleDefault], "docs") || !strings.Contains(m.status, "restart q") {
		t.Fatalf("loaded = %#v, status = %q", loaded, m.status)
	}
}

func TestMCPCtrlSDoesNotApplyOrSave(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	m := newModel(context.Background(), store, nil)
	m.config = config.Default()
	m.workspaceStore = &workspace.Store{Root: t.TempDir()}
	updated, _ := m.enterMCP()
	m = updated.(model)
	m.mcpDraft.Servers["docs"] = mcpconfig.ServerConfig{Transport: mcpconfig.TransportStdio, Command: "docs-server"}
	updated, command := m.updateMCP(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = updated.(model)
	if command != nil || m.mcpBusy {
		t.Fatalf("ctrl+s started a save: command=%v busy=%v", command != nil, m.mcpBusy)
	}
	if _, err := (mcpconfig.Store{Dir: store.Dir}).Load(); err == nil {
		t.Fatal("ctrl+s persisted the MCP draft")
	}

	m.mcpMode = mcpModeEditServer
	updated, command = m.updateMCPForm(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = updated.(model)
	if command != nil || m.mcpMode != mcpModeEditServer {
		t.Fatalf("ctrl+s applied the form: command=%v mode=%v", command != nil, m.mcpMode)
	}
}

func TestMCPFormStoresOnlyEnvironmentVariableReferences(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.mcpDraft = mcpconfig.Default()
	m.mcpMode = mcpModeEditServer
	values := []string{"remote", "streamable-http", "https://example.test/mcp", "[]", "{}", `{"Authorization":"MCP_AUTH"}`}
	for index, value := range values {
		m.mcpInputs[index].SetValue(value)
	}
	updated, _ := m.acceptMCPForm()
	m = updated.(model)
	server := m.mcpDraft.Servers["remote"]
	if server.Headers["Authorization"] != "MCP_AUTH" || server.URL != "https://example.test/mcp" {
		t.Fatalf("server = %#v", server)
	}
	m.mcpMode = mcpModeEditServer
	m.mcpInputs[0].SetValue("bad")
	m.mcpInputs[1].SetValue("stdio")
	m.mcpInputs[2].SetValue("server")
	m.mcpInputs[3].SetValue("[]")
	m.mcpInputs[4].SetValue(`{"TOKEN":"literal secret"}`)
	m.mcpInputs[5].SetValue("{}")
	updated, _ = m.acceptMCPForm()
	m = updated.(model)
	if !strings.Contains(m.status, "environment variable names") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestMCPViewUsesRolesAndKeepsAssignmentTargetVisible(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.mcpDraft = mcpconfig.Default()
	m.mcpPanel = 1
	m.width, m.height = 100, 40
	view := m.viewMCPLists()
	if !strings.Contains(view, "ROLES") || strings.Contains(view, "MODELS") ||
		!strings.Contains(view, "• default (main chat)") {
		t.Fatalf("role assignment view = %q", view)
	}
}

type roleCatalogTools struct {
	toolsByRole map[string][]client.Tool
	calls       []client.ToolCall
}

func (r *roleCatalogTools) Tools() []client.Tool { return nil }
func (r *roleCatalogTools) ToolsForRole(role string) []client.Tool {
	return append([]client.Tool(nil), r.toolsByRole[role]...)
}
func (r *roleCatalogTools) Environment() qtools.HostEnvironment { return qtools.HostEnvironment{} }
func (r *roleCatalogTools) Call(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
	r.calls = append(r.calls, call)
	return client.ToolResult{Content: "ok"}, nil
}

func TestScopedToolRuntimeRejectsToolsFromOtherRoles(t *testing.T) {
	tool := func(name string) client.Tool {
		return client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: name}}
	}
	base := &roleCatalogTools{toolsByRole: map[string][]client.Tool{
		"coder": {tool("coder_tool")}, "scout": {tool("scout_tool")},
	}}
	scoped := scopeTools(base, "coder")
	if len(scoped.Tools()) != 1 || scoped.Tools()[0].Function.Name != "coder_tool" {
		t.Fatalf("Tools() = %#v", scoped.Tools())
	}
	if _, err := scoped.Call(context.Background(), client.ToolCall{Function: client.FunctionCall{Name: "scout_tool"}}); err == nil {
		t.Fatal("call to another role's tool succeeded")
	}
	if _, err := scoped.Call(context.Background(), client.ToolCall{Function: client.FunctionCall{Name: "coder_tool"}}); err != nil {
		t.Fatal(err)
	}
}

func TestStandaloneMCPScreenQuitsOnEscape(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.standalone = true
	m.standaloneRoot = screenMCP
	m.screen = screenMCP
	m.mcpDraft = mcpconfig.Default()
	m.mcpOriginal = mcpconfig.Default()
	if _, command := m.updateMCP(tea.KeyPressMsg{Code: tea.KeyEsc}); command == nil {
		t.Fatal("standalone MCP escape did not quit")
	}
}
