package app

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
)

func TestSubagentScreenBindsBuiltinExternalAndSaves(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test-model"
	value.Agents.Connections = map[string]config.AgentConnectionConfig{"codex-main": {Preset: "codex"}}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	m := newModel(t.Context(), store, nil)
	m.config = value
	m.resize(100, 36)
	updated, _ := m.enterCustom()
	m = updated.(model)
	for index, definition := range m.custom.fixed {
		if definition.Info.Name == subagent.BuiltinWebSearchID {
			m.custom.cursor = index
			break
		}
	}
	updated, _ = m.beginCustomEdit(false)
	m = updated.(model)
	m.custom.inputs[customFieldACP].SetValue("codex-main")
	updated, command := m.saveCustom()
	m = updated.(model)
	if command == nil {
		t.Fatal("binding save command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Agents.Roles[config.AgentRoleSearch].Agent != "codex-main" {
		t.Fatalf("search binding = %#v", loaded.Agents.Roles[config.AgentRoleSearch])
	}
	if !strings.Contains(m.customSelectionDetail(), "ACP     codex-main") {
		t.Fatal(m.customSelectionDetail())
	}
}

func TestSubagentScreenRegistersACPConnection(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test-model"
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	m := newModel(t.Context(), store, nil)
	m.config = value
	m.resize(100, 36)
	updated, _ := m.enterCustom()
	m = updated.(model)
	updated, _ = m.enterCustomConnections()
	m = updated.(model)
	updated, _ = m.beginAgentConnectionAdd()
	m = updated.(model)
	fields := []string{"custom", "", "my-acp", `["stdio"]`, `{"TOKEN":"value"}`, ""}
	for index, field := range fields {
		m.agentsInputs[index].SetValue(field)
	}
	updated, command := m.acceptAgentsForm()
	m = updated.(model)
	if command == nil {
		t.Fatal("connection save command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	connection := m.config.Agents.Connections["custom"]
	if connection.Command != "my-acp" || len(connection.Args) != 1 || connection.Env["TOKEN"] != "value" {
		t.Fatalf("connection = %#v status=%q", connection, m.status)
	}
	if !strings.Contains(m.viewCustom(), "ACP Connections") || !strings.Contains(m.viewCustom(), "custom") {
		t.Fatal(m.viewCustom())
	}
}

func TestACPConnectionFormCtrlSDoesNotSave(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.agentsMode = agentsModeEditConnection
	updated, command := m.updateAgentsForm(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = updated.(model)
	if command != nil || m.agentsMode != agentsModeEditConnection {
		t.Fatalf("ctrl+s applied the form: command=%v mode=%v", command != nil, m.agentsMode)
	}
}

func TestACPConnectionIDIsImmutable(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.agentsDraft = config.Default()
	m.agentsDraft.Agents.Connections = map[string]config.AgentConnectionConfig{"original": {Preset: "codex"}}
	m.agentsEditID = "original"
	m.agentsMode = agentsModeEditConnection
	fields := []string{"renamed", "codex", "", `[]`, `{}`, ""}
	for index, field := range fields {
		m.agentsInputs[index].SetValue(field)
	}
	updated, command := m.acceptAgentsForm()
	m = updated.(model)
	if !strings.Contains(m.status, "cannot change") || m.agentsDraft.Agents.Connections["original"].Preset != "codex" {
		t.Fatalf("rename accepted: command=%v status=%q", command != nil, m.status)
	}
}

func TestACPConnectionDeleteRejectsSubagentReference(t *testing.T) {
	m := customViewFixture(t, 100, 36, true)
	m.config.Agents.Connections = map[string]config.AgentConnectionConfig{"browser": {Preset: "codex"}}
	m.agentsDraft = cloneConfigForAgents(m.config)
	profile := subagent.Profile{
		Version: 1, Name: "browser-check", Kind: subagent.AgentKindExternal, Agent: "browser",
		SystemPrompt: "Check the browser.", Tools: []string{}, Delegates: []string{},
	}
	if err := m.customStore().Save(profile, "global", nil); err != nil {
		t.Fatal(err)
	}
	updated, command := m.deleteAgentConnection()
	m = updated.(model)
	if command != nil || !strings.Contains(m.status, "global/browser-check") {
		t.Fatalf("referenced connection deleted: command=%v status=%q", command != nil, m.status)
	}
}

func TestAgentConnectionProbeResultUpdatesStatus(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.agentsBusy = true
	m.agentsProbe = map[string]string{"codex": "testing"}
	updated, _ := m.Update(agentConnectionProbedMsg{id: "codex"})
	m = updated.(model)
	if m.agentsBusy || m.agentsProbe["codex"] != "connected" || !strings.Contains(m.status, "lifecycle passed") {
		t.Fatalf("busy=%v probe=%q status=%q", m.agentsBusy, m.agentsProbe["codex"], m.status)
	}
}
