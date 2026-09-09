package main

import (
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

func TestStandaloneUICommandNames(t *testing.T) {
	for _, name := range []string{"model", "mcp", "agents", "skills", "ignore", "lsp", "help"} {
		if standaloneUICommand(name) == nil {
			t.Fatalf("standalone command %q is not registered", name)
		}
	}
	if standaloneUICommand("unknown") != nil {
		t.Fatal("unknown standalone command was registered")
	}
}
