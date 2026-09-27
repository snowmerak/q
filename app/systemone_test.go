package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/systemoneconfig"
)

func TestSystemOneSettingsOpenSaveAndReload(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	m := newModel(context.Background(), store, nil)
	updated, _ := m.enterSystemOne()
	m = updated.(model)
	if m.screen != screenSystemOne || m.systemOneInputs[0].Value() != systemoneconfig.DefaultURI {
		t.Fatalf("initial screen = %v, URI = %q", m.screen, m.systemOneInputs[0].Value())
	}
	m.systemOneInputs[0].SetValue("https://example.test/v1/systemone")
	m.systemOneInputs[1].SetValue("private-test-key")
	m.systemOneInputs[2].SetValue("jev-preview")
	if strings.Contains(ansi.Strip(m.viewSystemOne()), "private-test-key") {
		t.Fatal("API key appeared in the settings screen")
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = updated.(model)
	if !strings.Contains(m.status, "saved") {
		t.Fatalf("save status = %q", m.status)
	}
	loaded, err := (systemoneconfig.Store{Dir: store.Dir}).LoadOrDefault()
	if err != nil || loaded.URI != "https://example.test/v1/systemone" || loaded.APIKey != "private-test-key" || loaded.Model != "jev-preview" {
		t.Fatalf("saved settings = %#v, %v", loaded, err)
	}
	m.systemOneInputs[1].SetValue("unsaved-key")
	updated, _ = m.enterSystemOne()
	m = updated.(model)
	if m.systemOneInputs[1].Value() != "private-test-key" {
		t.Fatal("reopening did not reload the saved API key")
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEsc})
	if updated.(model).screen != screenChat {
		t.Fatal("escape did not return to chat")
	}
}

func TestSystemOneInvalidURIDoesNotOverwriteSettings(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	m := newModel(context.Background(), store, nil)
	updated, _ := m.enterSystemOne()
	m = updated.(model)
	m.systemOneInputs[0].SetValue("https://example.test/v1/models")
	updated, _ = m.saveSystemOne()
	m = updated.(model)
	if !strings.Contains(m.status, "URI") {
		t.Fatalf("validation status = %q", m.status)
	}
	if _, err := os.Stat((systemoneconfig.Store{Dir: store.Dir}).Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid settings wrote a file: %v", err)
	}
}

func TestSystemOneSlashCommandOpensSettings(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(config.Default(), &fakeClient{})
	m.input.SetValue("/systemone")
	updated, _ := m.updateChatKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if updated.(model).screen != screenSystemOne {
		t.Fatal("/systemone did not open settings")
	}
}

func TestSystemOneStandaloneEscapeQuits(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	updated, _ := m.enterSystemOne()
	m = updated.(model)
	m.standalone, m.standaloneRoot = true, screenSystemOne
	_, command := m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEsc})
	if command == nil {
		t.Fatal("standalone settings did not quit on Escape")
	}
}
