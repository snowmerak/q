package app

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
)

func TestLibrarySettingsSaveNetworkPreservesAPIKeys(t *testing.T) {
	directory := t.TempDir()
	store := config.Store{Dir: directory}
	libraryStore := qlibrary.ConfigStore{Dir: directory}
	value, generated, err := libraryStore.CreateAPIKey(qlibrary.DefaultConfig(), "desktop", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	m := newModel(context.Background(), store, nil)
	m.enterLibrarySettings()
	if m.screen != screenLibrary {
		t.Fatalf("Library screen = %v", m.screen)
	}
	m.libraryHostInput.SetValue("0.0.0.0")
	m.libraryPortInput.SetValue("18182")
	updated, command := m.updateLibrary(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = updated.(model)
	if command == nil {
		t.Fatal("Library network save command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)

	saved, err := libraryStore.LoadOrDefault()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Host != "0.0.0.0" || saved.Port != 18182 {
		t.Fatalf("saved Library network = %s:%d", saved.Host, saved.Port)
	}
	if len(saved.APIKeys) != 1 || saved.APIKeys[0].ID != generated.Record.ID || len(value.APIKeys) != 1 {
		t.Fatalf("Library API keys changed while saving network: %#v", saved.APIKeys)
	}
	if m.librarySettings.Host != saved.Host || m.librarySettings.Port != saved.Port {
		t.Fatalf("Library screen state = %#v", m.librarySettings)
	}
}

func TestLibrarySettingsRejectPortZero(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterLibrarySettings()
	m.libraryHostInput.SetValue("127.0.0.1")
	m.libraryPortInput.SetValue("0")
	updated, _ := m.updateLibrary(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.status != "Port must be between 1 and 65535" {
		t.Fatalf("port-zero status = %q", m.status)
	}
	if _, err := m.librarySettingsStore.LoadOrDefault(); err != nil {
		t.Fatal(err)
	}
	if m.librarySettings.Port == 0 {
		t.Fatal("invalid port was applied to Library settings")
	}
}

func TestLibraryArrowNavigationFocusesEditablePort(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterLibrarySettings()
	m.libraryPortInput.SetValue("")
	updated, _ := m.updateLibrary(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(model)
	if m.libraryNetworkFocus != 1 || !m.libraryPortInput.Focused() || m.libraryHostInput.Focused() {
		t.Fatalf("Library input focus = index %d, host %v, port %v", m.libraryNetworkFocus, m.libraryHostInput.Focused(), m.libraryPortInput.Focused())
	}
	updated, _ = m.updateLibrary(tea.KeyPressMsg{Code: '1', Text: "1"})
	m = updated.(model)
	if m.libraryPortInput.Value() != "1" {
		t.Fatalf("Library port input = %q", m.libraryPortInput.Value())
	}
}

func TestSlashLibraryOpensSettings(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.screen = screenChat
	m.client = &fakeClient{}
	m.input.SetValue("/library")

	updated, command := m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if command == nil {
		t.Fatal("deferred Library command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if m.screen != screenLibrary || m.input.Value() != "" {
		t.Fatalf("Library command state = screen %v, input %q", m.screen, m.input.Value())
	}
}

func TestStandaloneLibrarySettingsQuitOnEscape(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterLibrarySettings()
	m.standalone = true
	m.standaloneRoot = screenLibrary
	if _, command := m.updateLibrary(tea.KeyPressMsg{Code: tea.KeyEsc}); command == nil {
		t.Fatal("standalone Library escape did not quit")
	}
}
