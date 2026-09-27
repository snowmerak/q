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

func systemOneTestKey(code rune, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Text: text}
}

func TestSystemOneEditsSaveAsTheyChange(t *testing.T) {
	store := systemoneconfig.Store{Dir: t.TempDir()}
	m := newModel(t.Context(), config.Store{Dir: store.Dir}, nil)
	updated, _ := m.enterSystemOne()
	m = updated.(model)
	if m.screen != screenSystemOne || m.systemOnePage != systemOnePageList || m.systemOneListCursor != systemOneProviderRow {
		t.Fatalf("initial screen = %v, page = %v, cursor = %d", m.screen, m.systemOnePage, m.systemOneListCursor)
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyTab})
	m = updated.(model)
	if m.systemOneListCursor != 0 {
		t.Fatalf("Tab did not wrap list selection: %d", m.systemOneListCursor)
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(model)
	m.systemOneInputs[systemOneFieldURI].SetValue("https://example.test/v1/systemon")
	updated, _ = m.updateSystemOne(systemOneTestKey('e', "e"))
	m = updated.(model)
	loaded, err := store.LoadOrDefault()
	if err != nil || loaded.Providers[0].URI != "https://example.test/v1/systemone" {
		t.Fatalf("URI was not saved on edit: %#v, %v", loaded, err)
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(model)
	m.systemOneInputs[systemOneFieldProviderKey].SetValue("private-test-ke")
	updated, _ = m.updateSystemOne(systemOneTestKey('y', "y"))
	m = updated.(model)
	loaded, err = store.LoadOrDefault()
	if err != nil || loaded.Providers[0].APIKey != "private-test-key" {
		t.Fatalf("API key was not saved on edit: %#v, %v", loaded, err)
	}
	if strings.Contains(ansi.Strip(m.viewSystemOne()), "private-test-key") {
		t.Fatal("API key appeared in the settings screen")
	}
	updated, _ = m.Update(tea.PasteMsg{Content: "-pasted"})
	m = updated.(model)
	loaded, err = store.LoadOrDefault()
	if err != nil || loaded.Providers[0].APIKey != "private-test-key-pasted" {
		t.Fatalf("pasted API key was not saved: %#v, %v", loaded, err)
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.systemOnePage != systemOnePageNetwork {
		t.Fatal("Enter did not open network settings")
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(model)
	m.systemOneInputs[systemOneFieldPort].SetValue("878")
	m.systemOneInputs[systemOneFieldPort].CursorEnd()
	updated, _ = m.updateSystemOne(systemOneTestKey('7', "7"))
	m = updated.(model)
	loaded, err = store.LoadOrDefault()
	if err != nil || loaded.Server.Port != 8787 {
		t.Fatalf("port was not saved on edit: %#v, %v", loaded, err)
	}
}

func TestSystemOnePublicHostSavesWithoutServerKey(t *testing.T) {
	store := systemoneconfig.Store{Dir: t.TempDir()}
	m := newModel(t.Context(), config.Store{Dir: store.Dir}, nil)
	updated, _ := m.enterSystemOne()
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	m.systemOneInputs[systemOneFieldHost].SetValue("0.0.0.")
	m.systemOneInputs[systemOneFieldHost].CursorEnd()
	updated, _ = m.updateSystemOne(systemOneTestKey('0', "0"))
	m = updated.(model)
	loaded, err := store.LoadOrDefault()
	if err != nil || loaded.Server.Host != "0.0.0.0" || loaded.Server.APIKey != "" {
		t.Fatalf("public host without server key was not saved: %#v, %v", loaded.Server, err)
	}
}

func TestSystemOneManagedAPIKeysGenerateAndRevoke(t *testing.T) {
	store := systemoneconfig.Store{Dir: t.TempDir()}
	m := newModel(t.Context(), config.Store{Dir: store.Dir}, nil)
	updated, _ := m.enterSystemOne()
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.systemOnePage != systemOnePageKeys {
		t.Fatal("API keys did not open")
	}
	updated, _ = m.updateSystemOne(systemOneTestKey('a', "a"))
	m = updated.(model)
	m.systemOneKeyAlias.SetValue("desktop")
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	secret := m.generatedSystemOneKey
	if !strings.HasPrefix(secret, "qso_") || m.systemOneConfig.ActiveKeyCount() != 1 {
		t.Fatalf("generated key state = %q, %#v", secret, m.systemOneConfig.APIKeys)
	}
	body, err := os.ReadFile(store.Path())
	if err != nil || strings.Contains(string(body), secret) {
		t.Fatalf("plaintext key was persisted: %v", err)
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.updateSystemOne(systemOneTestKey('r', "r"))
	m = updated.(model)
	if !m.systemOneKeyRevokeArmed {
		t.Fatal("revoke confirmation was not armed")
	}
	updated, _ = m.updateSystemOne(systemOneTestKey('r', "r"))
	m = updated.(model)
	if m.systemOneConfig.ActiveKeyCount() != 0 || m.systemOneConfig.APIKeys[0].RevokedAt == nil {
		t.Fatalf("key was not revoked: %#v", m.systemOneConfig.APIKeys)
	}
}

func TestSystemOneLegacyServerKeyCanBeRevoked(t *testing.T) {
	store := systemoneconfig.Store{Dir: t.TempDir()}
	value := systemoneconfig.Default()
	value.Server.APIKey = "old-client-key"
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	m := newModel(t.Context(), config.Store{Dir: store.Dir}, nil)
	updated, _ := m.enterSystemOne()
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.systemOnePage != systemOnePageKeys || m.systemOneConfig.ActiveKeyCount() != 1 {
		t.Fatal("legacy key did not appear in API keys")
	}
	updated, _ = m.updateSystemOne(systemOneTestKey('r', "r"))
	m = updated.(model)
	updated, _ = m.updateSystemOne(systemOneTestKey('r', "r"))
	m = updated.(model)
	loaded, err := store.LoadOrDefault()
	if err != nil || loaded.Server.APIKey != "" || loaded.ActiveKeyCount() != 0 {
		t.Fatalf("legacy key was not revoked: %#v, %v", loaded, err)
	}
}

func TestSystemOneInvalidEditDoesNotOverwriteSettings(t *testing.T) {
	store := systemoneconfig.Store{Dir: t.TempDir()}
	m := newModel(t.Context(), config.Store{Dir: store.Dir}, nil)
	updated, _ := m.enterSystemOne()
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(model)
	m.systemOneInputs[systemOneFieldURI].SetValue("https://example.test/v1/model")
	updated, _ = m.updateSystemOne(systemOneTestKey('s', "s"))
	m = updated.(model)
	if !m.systemOnePending || !strings.Contains(m.status, "URI") {
		t.Fatalf("invalid edit status = %q, pending = %v", m.status, m.systemOnePending)
	}
	if _, err := os.Stat(store.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid edit wrote a file: %v", err)
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(model)
	if m.systemOnePage != systemOnePageList || m.systemOneConfig.Providers[0].URI != systemoneconfig.DefaultURI {
		t.Fatal("Escape did not discard the invalid draft")
	}
}

func TestSystemOneProviderListUsesGatewayKeysAndPersists(t *testing.T) {
	store := systemoneconfig.Store{Dir: t.TempDir()}
	m := newModel(t.Context(), config.Store{Dir: store.Dir}, nil)
	updated, _ := m.enterSystemOne()
	m = updated.(model)
	updated, _ = m.updateSystemOne(systemOneTestKey('a', "a"))
	m = updated.(model)
	loaded, err := store.LoadOrDefault()
	if err != nil || len(loaded.Providers) != 2 || m.systemOnePage != systemOnePageProvider {
		t.Fatalf("provider was not added immediately: %#v, %v", loaded, err)
	}
	m.systemOneInputs[systemOneFieldProviderID].SetValue("secon")
	updated, _ = m.updateSystemOne(systemOneTestKey('d', "d"))
	m = updated.(model)
	loaded, err = store.LoadOrDefault()
	if err != nil || loaded.Providers[1].ID != "second" {
		t.Fatalf("provider edit was not saved: %#v, %v", loaded, err)
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(model)
	if m.systemOneListCursor != systemOneProviderRow+1 {
		t.Fatalf("provider list cursor = %d", m.systemOneListCursor)
	}
	updated, _ = m.updateSystemOne(systemOneTestKey('d', "d"))
	m = updated.(model)
	loaded, err = store.LoadOrDefault()
	if err != nil || len(loaded.Providers) != 1 || m.systemOnePage != systemOnePageList {
		t.Fatalf("provider was not deleted immediately: %#v, %v", loaded, err)
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.systemOnePage != systemOnePageProvider || m.systemOneInputs[systemOneFieldProviderID].Value() != "typesafe" {
		t.Fatal("Enter did not reopen selected provider")
	}
}

func TestSystemOneModelPickerSavesDefaultAndRoleSelection(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/models" || request.Header.Get("Authorization") != "Bearer first-key" {
			t.Errorf("first model request = %s %s, auth %q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
		}
		_, _ = writer.Write([]byte(`{"models":[{"name":"first-model"}]}`))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer second-key" {
			t.Errorf("second authorization = %q", request.Header.Get("Authorization"))
		}
		_, _ = writer.Write([]byte(`{"models":[{"name":"second-model"}]}`))
	}))
	defer second.Close()

	store := systemoneconfig.Store{Dir: t.TempDir()}
	value := systemoneconfig.Default()
	value.Providers[0].URI = first.URL + "/v1/systemone"
	value.Providers[0].APIKey = "first-key"
	value.DefaultModel = "typesafe/first-model"
	value.Providers = append(value.Providers, systemoneconfig.ProviderConfig{
		ID: "second", URI: second.URL + "/v1/systemone", APIKey: "second-key",
	})
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	m := newModel(t.Context(), config.Store{Dir: store.Dir}, nil)
	updated, _ := m.enterSystemOne()
	m = updated.(model)
	m.systemOneListCursor = 0
	updated, command := m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if !m.systemOneLoading || command == nil {
		t.Fatal("Enter did not start model discovery")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if !m.systemOnePicking || len(m.systemOneModels) != 2 {
		t.Fatalf("default models = %#v, status = %q", m.systemOneModels, m.status)
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	loaded, err := store.LoadOrDefault()
	if err != nil || loaded.DefaultModel != "second/second-model" {
		t.Fatalf("default choice was not saved: %#v, %v", loaded, err)
	}
	m.systemOneListCursor = 1
	updated, command = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(command())
	m = updated.(model)
	if len(m.systemOneModels) != 3 || m.systemOneModels[0].Name != "" {
		t.Fatalf("role models = %#v", m.systemOneModels)
	}
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	loaded, err = store.LoadOrDefault()
	if err != nil || loaded.RoleModels[systemoneconfig.RoleAgentSkillDecision] != "typesafe/first-model" {
		t.Fatalf("role choice was not saved: %#v, %v", loaded, err)
	}
}

func TestSystemOneProviderRenameUpdatesAssignmentsOnEdit(t *testing.T) {
	store := systemoneconfig.Store{Dir: t.TempDir()}
	value := systemoneconfig.Default()
	value.RoleModels = map[string]string{systemoneconfig.RoleAgentSkillDecision: "typesafe/skill-model"}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	m := newModel(t.Context(), config.Store{Dir: store.Dir}, nil)
	updated, _ := m.enterSystemOne()
	m = updated.(model)
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.updateSystemOne(systemOneTestKey('x', "x"))
	m = updated.(model)
	loaded, err := store.LoadOrDefault()
	if err != nil || loaded.Providers[0].ID != "typesafex" || loaded.DefaultModel != "typesafex/jev-latest" ||
		loaded.RoleModels[systemoneconfig.RoleAgentSkillDecision] != "typesafex/skill-model" {
		t.Fatalf("renamed assignments = %#v, %v", loaded, err)
	}
}

func TestSystemOneModelPickerCancelIgnoresLateResult(t *testing.T) {
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	updated, _ := m.enterSystemOne()
	m = updated.(model)
	m.systemOneLoading = true
	m.systemOneRequestID = 7
	m.systemOneFocus = systemOneFieldDefaultModel
	updated, _ = m.updateSystemOne(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(model)
	updated, _ = m.Update(systemOneModelsMsg{requestID: 7})
	m = updated.(model)
	if m.systemOnePicking || m.systemOneLoading || m.screen != screenSystemOne {
		t.Fatal("canceled model request changed the settings screen")
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
