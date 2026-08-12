package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/config"
	qlsp "github.com/snowmerak/q/lsp"
	"github.com/snowmerak/q/workspace"
)

func TestLSPSettingsAddDiscoverAndSave(t *testing.T) {
	globalStore := config.Store{Dir: filepath.Join(t.TempDir(), ".q")}
	value := config.Default()
	value.Provider.Model = "test-model"
	if err := globalStore.Save(value); err != nil {
		t.Fatal(err)
	}
	workspaceStore := workspace.Store{Root: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(workspaceStore.Root, "services", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceStore.Root, "services", "api", "go.mod"), []byte("module api\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newModel(context.Background(), globalStore, nil)
	m.config = value
	m.workspaceStore = &workspaceStore
	updated, _ := m.enterLSP()
	m = updated.(model)
	updated, _ = m.beginLSPAdd()
	m = updated.(model)
	m.lspInputs[0].SetValue("gopls")
	m.lspInputs[1].SetValue("go")
	m.lspInputs[2].SetValue("gopls")
	m.lspInputs[3].SetValue(`["serve"]`)
	updated, _ = m.acceptLSPForm()
	m = updated.(model)
	if m.lspDraftGlobal.Languages["go"] != "gopls" {
		t.Fatalf("language defaults = %#v", m.lspDraftGlobal.Languages)
	}
	m.lspPanel = 1
	updated, command := m.startLSPDiscovery()
	m = updated.(model)
	updated, _ = m.Update(command())
	m = updated.(model)
	if len(m.lspDraftWorkspace.Roots) != 1 || m.lspDraftWorkspace.Roots[0].Path != "services/api" {
		t.Fatalf("roots = %#v", m.lspDraftWorkspace.Roots)
	}
	updated, _ = m.saveLSPSettings()
	m = updated.(model)
	if !strings.Contains(m.status, "saved") {
		t.Fatalf("status = %q", m.status)
	}

	loaded, err := globalStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LSP.Servers["gopls"].Command != "gopls" || loaded.LSP.Languages["go"] != "gopls" {
		t.Fatalf("global LSP = %#v", loaded.LSP)
	}
	roots, err := workspaceStore.LoadLSP()
	if err != nil {
		t.Fatal(err)
	}
	if len(roots.Roots) != 1 || roots.Roots[0].Source != qlsp.RootSourceDiscovered {
		t.Fatalf("saved roots = %#v", roots)
	}
}

func TestLSPServerRenameUpdatesDefaultsAndRootOverrides(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.screen = screenLSP
	m.lspDraftGlobal = qlsp.GlobalConfig{
		Servers:   map[string]qlsp.ServerConfig{"old": {Languages: []string{"go"}, Command: "gopls"}},
		Languages: map[string]string{"go": "old"},
	}
	m.lspDraftWorkspace = qlsp.WorkspaceConfig{Version: qlsp.WorkspaceConfigVersion, Roots: []qlsp.RootConfig{{Path: ".", Language: "go", Server: "old"}}}
	updated, _ := m.beginLSPEdit()
	m = updated.(model)
	m.lspInputs[0].SetValue("new")
	updated, _ = m.acceptLSPForm()
	m = updated.(model)
	if _, exists := m.lspDraftGlobal.Servers["old"]; exists || m.lspDraftGlobal.Languages["go"] != "new" || m.lspDraftWorkspace.Roots[0].Server != "new" {
		t.Fatalf("renamed global = %#v, roots = %#v", m.lspDraftGlobal, m.lspDraftWorkspace.Roots)
	}
}

func TestStandaloneLSPEscapeAndView(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.standalone = true
	m.standaloneRoot = screenLSP
	m.screen = screenLSP
	m.lspDraftGlobal = qlsp.GlobalConfig{Servers: map[string]qlsp.ServerConfig{"gopls": {Languages: []string{"go"}, Command: "gopls"}}, Languages: map[string]string{"go": "gopls"}}
	m.lspDraftWorkspace = qlsp.WorkspaceConfig{Version: qlsp.WorkspaceConfigVersion}
	m.lspOriginalGlobal = cloneLSPGlobal(m.lspDraftGlobal)
	m.lspOriginalWorkspace = cloneLSPWorkspace(m.lspDraftWorkspace)
	if view := m.viewLSP(); !strings.Contains(view, "GLOBAL SERVERS") || !strings.Contains(view, "gopls") {
		t.Fatalf("view = %s", view)
	}
	if _, command := m.updateLSP(tea.KeyPressMsg{Code: tea.KeyEsc}); command == nil {
		t.Fatal("standalone LSP escape did not quit")
	}
}
