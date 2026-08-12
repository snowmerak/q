package lsp

import (
	"errors"
	"reflect"
	"testing"
)

func TestDiscoverServersSelectsInstalledPreferredCandidates(t *testing.T) {
	installed := map[string]string{
		"gopls":                      "/bin/gopls",
		"basedpyright-langserver":    "/bin/basedpyright-langserver",
		"pyright-langserver":         "/bin/pyright-langserver",
		"typescript-language-server": "/bin/typescript-language-server",
	}
	lookPath := func(command string) (string, error) {
		if path := installed[command]; path != "" {
			return path, nil
		}
		return "", errors.New("not found")
	}
	got := discoverServers([]string{"Go", "python"}, lookPath)
	if len(got) != 2 || got[0].ID != "gopls" || got[1].ID != "basedpyright" {
		t.Fatalf("servers = %#v", got)
	}
	if !reflect.DeepEqual(got[0].Config.Args, []string{"serve"}) || got[0].Path != "/bin/gopls" {
		t.Fatalf("gopls = %#v", got[0])
	}
}

func TestDiscoverServersChecksAllLanguagesAndDeduplicatesSharedServer(t *testing.T) {
	lookPath := func(command string) (string, error) {
		if command == "typescript-language-server" {
			return "/bin/tsls", nil
		}
		return "", errors.New("not found")
	}
	got := discoverServers(nil, lookPath)
	if len(got) != 1 || got[0].ID != "typescript" || !reflect.DeepEqual(got[0].Config.Languages, []string{"typescript", "javascript"}) {
		t.Fatalf("servers = %#v", got)
	}
}
