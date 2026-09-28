package studio

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/snowmerak/q/config"
	qlsp "github.com/snowmerak/q/lsp"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/subagent"
)

func TestIntegrationAPIMCPAndLSPRoundTrip(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	if err := store.Save(configuredIntegrationTestConfig()); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/studio\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(store)
	if err != nil {
		t.Fatal(err)
	}

	mcpValue := mcpconfig.Default()
	mcpValue.Servers["docs"] = mcpconfig.ServerConfig{
		Transport: mcpconfig.TransportStreamableHTTP,
		URL:       "https://example.test/mcp",
		Headers:   map[string]string{"Authorization": "DOCS_TOKEN"},
	}
	mcpValue.Roles[mcpconfig.RoleDefault] = []string{"docs"}
	response := serveJSON(t, handler, http.MethodPut, "/api/v1/integrations/mcp", mcpValue)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT MCP = %d %s", response.Code, response.Body.String())
	}
	loadedMCP, err := (mcpconfig.Store{Dir: store.Dir}).Load()
	if err != nil || loadedMCP.Servers["docs"].Headers["Authorization"] != "DOCS_TOKEN" {
		t.Fatalf("saved MCP = %#v, %v", loadedMCP, err)
	}

	lspValue := lspSettingsUpdate{
		WorkspaceRoot: root,
		Global: qlsp.GlobalConfig{
			Servers:   map[string]qlsp.ServerConfig{"gopls": {Languages: []string{"go"}, Command: "gopls", Args: []string{"serve"}}},
			Languages: map[string]string{"go": "gopls"},
		},
		Workspace: qlsp.WorkspaceConfig{Version: qlsp.WorkspaceConfigVersion, Roots: []qlsp.RootConfig{{Path: ".", Language: "go", Source: qlsp.RootSourceManual}}},
	}
	response = serveJSON(t, handler, http.MethodPut, "/api/v1/workspaces/lsp", lspValue)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT LSP = %d %s", response.Code, response.Body.String())
	}
	loadedConfig, err := store.Load()
	if err != nil || loadedConfig.LSP.Languages["go"] != "gopls" {
		t.Fatalf("saved global LSP = %#v, %v", loadedConfig.LSP, err)
	}
	response = serveJSON(t, handler, http.MethodPost, "/api/v1/workspaces/lsp/discover", workspaceRequest{WorkspaceRoot: root})
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"language":"go"`)) {
		t.Fatalf("discover LSP = %d %s", response.Code, response.Body.String())
	}
}

func TestIntegrationAPIIgnoreUsesRevisionConflicts(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	if err := store.Save(configuredIntegrationTestConfig()); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	handler, err := newHandler(store)
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/ignore?workspace_root="+url.QueryEscape(root), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET ignore = %d %s", response.Code, response.Body.String())
	}
	var current ignoreSettingsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	update := ignoreUpdate{WorkspaceRoot: root, Content: "dist/\n", Revision: current.Revision}
	response = serveJSON(t, handler, http.MethodPut, "/api/v1/workspaces/ignore", update)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT ignore = %d %s", response.Code, response.Body.String())
	}
	body, err := os.ReadFile(filepath.Join(root, ".qignore"))
	if err != nil || string(body) != "dist/\n" {
		t.Fatalf("saved ignore = %q, %v", body, err)
	}
	response = serveJSON(t, handler, http.MethodPut, "/api/v1/workspaces/ignore", update)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale PUT ignore = %d %s", response.Code, response.Body.String())
	}
}

func TestIntegrationAPISkillsListsPortableEntries(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	if err := store.Save(configuredIntegrationTestConfig()); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	skillDirectory := filepath.Join(root, ".agents", "skills", "read-go")
	if err := os.MkdirAll(skillDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: read-go\ndescription: Read Go code carefully.\ntags: [go, review]\n---\n\nInspect the requested package.\n"
	if err := os.WriteFile(filepath.Join(skillDirectory, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/skills?workspace_root="+url.QueryEscape(root), nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"name":"read-go"`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"managed":false`)) {
		t.Fatalf("GET skills = %d %s", response.Code, response.Body.String())
	}
}

func TestIntegrationAPIAgentConnectionsAndProfiles(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	if err := store.Save(configuredIntegrationTestConfig()); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	handler, err := newHandler(store)
	if err != nil {
		t.Fatal(err)
	}

	connections := agentConnectionsUpdate{
		WorkspaceRoot: root,
		Connections:   map[string]config.AgentConnectionConfig{"codex-local": {Preset: "codex", Env: map[string]string{"TOKEN": "super-secret"}}},
		Bindings:      map[string]string{config.AgentRoleSearch: "codex-local"},
	}
	response := serveJSON(t, handler, http.MethodPut, "/api/v1/workspaces/agents", connections)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT agents = %d %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("super-secret")) || !bytes.Contains(response.Body.Bytes(), []byte(redactedConnectionSecret)) {
		t.Fatalf("agent response exposed or failed to redact connection env: %s", response.Body.String())
	}
	profile := profileUpdate{
		WorkspaceRoot: root, Scope: "workspace",
		Profile: subagent.Profile{
			Version: 1, Name: "inspector", Description: "Inspect focused code.", Kind: subagent.AgentKindInner,
			Role: config.AgentRoleResearch, SystemPrompt: "Inspect the assigned code and report evidence.",
			Tools: []string{"read_file"}, Delegates: []string{subagent.BuiltinResearchID},
		},
	}
	response = serveJSON(t, handler, http.MethodPost, "/api/v1/workspaces/agents/profiles", profile)
	if response.Code != http.StatusOK {
		t.Fatalf("POST profile = %d %s", response.Code, response.Body.String())
	}
	var snapshot agentSettingsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Bindings[config.AgentRoleSearch] != "codex-local" || len(snapshot.Profiles) != 1 || snapshot.Profiles[0].Profile.Name != "inspector" {
		t.Fatalf("agent snapshot = %#v", snapshot)
	}
	deleteRequest := profileUpdate{WorkspaceRoot: root, Scope: "workspace", Revision: snapshot.Profiles[0].Revision}
	response = serveJSON(t, handler, http.MethodDelete, "/api/v1/workspaces/agents/profiles/inspector", deleteRequest)
	if response.Code != http.StatusOK {
		t.Fatalf("DELETE profile = %d %s", response.Code, response.Body.String())
	}
}

func serveJSON(t *testing.T, handler http.Handler, method, target string, value any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func configuredIntegrationTestConfig() config.Config {
	value := config.Default()
	value.Provider.Model = "test/model"
	return value
}
