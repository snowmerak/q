package main

import (
	"net/url"
	"slices"
	"testing"
)

func TestParseServiceCommand(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		mode      serviceCommandMode
		startArgs []string
		ok        bool
	}{
		{name: "bare command configures", mode: serviceCommandConfigure, ok: true},
		{name: "start runs service", args: []string{"start"}, mode: serviceCommandStart, ok: true},
		{name: "start preserves options", args: []string{"start", "--port", "0"}, mode: serviceCommandStart, startArgs: []string{"--port", "0"}, ok: true},
		{name: "old config subcommand is rejected", args: []string{"config"}},
		{name: "bare options are rejected", args: []string{"--port", "0"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode, startArgs, ok := parseServiceCommand(test.args)
			if mode != test.mode || ok != test.ok || !slices.Equal(startArgs, test.startArgs) {
				t.Fatalf("parseServiceCommand(%q) = (%v, %q, %v), want (%v, %q, %v)", test.args, mode, startArgs, ok, test.mode, test.startArgs, test.ok)
			}
		})
	}
}

func TestStudioUICommandPaths(t *testing.T) {
	for _, name := range []string{"model", "mcp", "subagents", "agents", "skills", "ignore", "lsp", "help"} {
		if studioUIPath(name) == "" {
			t.Fatalf("standalone command %q is not registered", name)
		}
	}
	if studioUIPath("unknown") != "" {
		t.Fatal("unknown standalone command was registered")
	}
}

func TestStudioWorkspacePathPreservesRouteAndCanonicalQueryValues(t *testing.T) {
	path := studioWorkspacePath("/settings", "integrations&panel=skills", `C:\repo with spaces`)
	parsed, err := url.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/settings" || parsed.Query().Get("section") != "integrations" || parsed.Query().Get("panel") != "skills" || parsed.Query().Get("workspace_root") != `C:\repo with spaces` {
		t.Fatalf("Studio route = %q (%#v)", path, parsed.Query())
	}
}
