package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
	if err != nil || loaded.Providers[0].URI != "https://example.test/v1/systemone" || loaded.Providers[0].APIKey != "private-test-key" || loaded.Providers[0].Model != "jev-preview" {
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

func TestSystemOneModelPickerUsesDraftConnectionAndSavesSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/models" || request.Header.Get("Authorization") != "Bearer draft-key" {
			t.Errorf("model request = %s %s, auth %q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"models":[{"name":"jev-latest","description":"Stable"},{"name":"jev-preview","description":"Preview"}]}`))
	}))
	defer server.Close()

	store := config.Store{Dir: t.TempDir()}
	m := newModel(t.Context(), store, nil)
	updated, _ := m.enterSystemOne()
	m = updated.(model)
	m.systemOneInputs[0].SetValue(server.URL + "/v1/systemone")
	m.systemOneInputs[1].SetValue("draft-key")
	m.systemOneFocus = 2
	updated, command := m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if !m.systemOneLoading || command == nil {
		t.Fatal("Enter did not start model discovery")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if !m.systemOnePicking || len(m.systemOneModels) != 2 || !strings.Contains(ansi.Strip(m.viewSystemOne()), "jev-preview") {
		t.Fatalf("model picker = picking %v, models %#v, status %q", m.systemOnePicking, m.systemOneModels, m.status)
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.systemOnePicking || m.systemOneInputs[2].Value() != "jev-preview" {
		t.Fatalf("selected model = %q", m.systemOneInputs[2].Value())
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = updated.(model)
	value, err := (systemoneconfig.Store{Dir: store.Dir}).LoadOrDefault()
	if err != nil || value.Providers[0].Model != "jev-preview" || value.Providers[0].URI != server.URL+"/v1/systemone" || value.Providers[0].APIKey != "draft-key" {
		t.Fatalf("saved model selection = %#v, %v", value, err)
	}
}

func TestSystemOneModelPickerCancelIgnoresLateResult(t *testing.T) {
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	updated, _ := m.enterSystemOne()
	m = updated.(model)
	m.systemOneLoading = true
	m.systemOneRequestID = 7
	m.systemOneFocus = 2
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(model)
	updated, _ = m.Update(systemOneModelsMsg{requestID: 7})
	m = updated.(model)
	if m.systemOnePicking || m.systemOneLoading || m.screen != screenSystemOne {
		t.Fatal("canceled model request changed the settings screen")
	}
}

func TestSystemOneSettingsManageMultipleProvidersAndServer(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	m := newModel(t.Context(), store, nil)
	updated, _ := m.enterSystemOne()
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	m = updated.(model)
	if len(m.systemOneConfig.Providers) != 2 || m.systemOneProvider != 1 {
		t.Fatalf("added providers = %#v", m.systemOneConfig.Providers)
	}
	m.systemOneInputs[3].SetValue("second")
	m.systemOneInputs[0].SetValue("https://second.example/v1/systemone")
	m.systemOneInputs[1].SetValue("second-key")
	m.systemOneInputs[2].SetValue("second-model")
	m.systemOneInputs[5].SetValue("127.0.0.1")
	m.systemOneInputs[6].SetValue("8787")
	m.systemOneInputs[7].SetValue("client-key")
	if strings.Contains(ansi.Strip(m.viewSystemOne()), "second-key") || strings.Contains(ansi.Strip(m.viewSystemOne()), "client-key") {
		t.Fatal("a secret appeared in the System One screen")
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	m = updated.(model)
	if m.systemOneProvider != 0 {
		t.Fatalf("provider switch = %d", m.systemOneProvider)
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	m = updated.(model)
	if m.systemOneInputs[3].Value() != "second" || m.systemOneInputs[1].Value() != "second-key" {
		t.Fatal("provider draft was lost while switching")
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = updated.(model)
	value, err := (systemoneconfig.Store{Dir: store.Dir}).LoadOrDefault()
	if err != nil || len(value.Providers) != 2 || value.Selected != "second" ||
		value.Server.Port != 8787 || value.Server.APIKey != "client-key" ||
		value.Providers[1].URI != "https://second.example/v1/systemone" {
		t.Fatalf("saved settings = %#v, %v", value, err)
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = updated.(model)
	value, err = (systemoneconfig.Store{Dir: store.Dir}).LoadOrDefault()
	if err != nil || len(value.Providers) != 1 || value.Selected != "typesafe" {
		t.Fatalf("provider removal = %#v, %v", value, err)
	}
}
