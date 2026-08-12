package main

import "testing"

func TestStandaloneUICommandNames(t *testing.T) {
	for _, name := range []string{"model", "skills", "ignore", "lsp", "help"} {
		if standaloneUICommand(name) == nil {
			t.Fatalf("standalone command %q is not registered", name)
		}
	}
	if standaloneUICommand("unknown") != nil {
		t.Fatal("unknown standalone command was registered")
	}
}
