package lsp

import (
	"strings"
	"testing"
)

func TestSettingsValidationAndResolution(t *testing.T) {
	global := GlobalConfig{Servers: map[string]ServerConfig{
		"gopls": {Languages: []string{"go"}, Command: "gopls"},
		"web":   {Languages: []string{"typescript", "javascript"}, Command: "typescript-language-server", Args: []string{"--stdio"}},
	}, Languages: map[string]string{"go": "gopls", "typescript": "web", "javascript": "web"}}
	if err := global.Validate(); err != nil {
		t.Fatal(err)
	}
	workspace := WorkspaceConfig{Version: WorkspaceConfigVersion, Roots: []RootConfig{
		{Path: "services/api", Language: "go", Source: RootSourceManual},
		{Path: "web", Language: "typescript", Server: "web", Source: RootSourceDiscovered},
	}}
	if err := workspace.Validate(global); err != nil {
		t.Fatal(err)
	}
	if server, ok := ResolveServer(global, workspace.Roots[0]); !ok || server != "gopls" {
		t.Fatalf("resolved server = %q, %v", server, ok)
	}
}

func TestSettingsRejectInvalidRootAndServer(t *testing.T) {
	global := GlobalConfig{Servers: map[string]ServerConfig{"gopls": {Languages: []string{"go"}, Command: "gopls"}}}
	tests := []RootConfig{
		{Path: "../outside", Language: "go"},
		{Path: "api", Language: "go", Server: "missing"},
		{Path: "api", Language: "python", Server: "gopls"},
	}
	for _, root := range tests {
		err := (WorkspaceConfig{Version: WorkspaceConfigVersion, Roots: []RootConfig{root}}).Validate(global)
		if err == nil {
			t.Fatalf("root %#v was accepted", root)
		}
	}
	if err := (GlobalConfig{Servers: map[string]ServerConfig{"bad id": {Languages: []string{"go"}, Command: "gopls"}}}).Validate(); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error = %v", err)
	}
}
