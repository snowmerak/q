package app

import (
	"context"
	"strings"
	"testing"

	"github.com/snowmerak/q/config"
)

func TestAgentsSettingsAssignSearchConnectionAndSave(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test-model"
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), store, nil)
	updated, _ := m.enterAgents()
	m = updated.(model)
	m.agentsDraft.Agents.Connections["codex-main"] = config.AgentConnectionConfig{Preset: "codex"}
	updated, _ = m.assignAgentRole()
	m = updated.(model)
	if m.agentsDraft.Agents.Roles[config.AgentRoleSearch].Agent != "codex-main" {
		t.Fatalf("search role = %#v", m.agentsDraft.Agents.Roles[config.AgentRoleSearch])
	}
	updated, command := m.saveAgentsSettings()
	m = updated.(model)
	if command == nil {
		t.Fatal("save command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Agents.Roles[config.AgentRoleSearch].Agent != "codex-main" ||
		loaded.Agents.Connections["codex-main"].Preset != "codex" ||
		!strings.Contains(m.status, "saved") {
		t.Fatalf("loaded=%#v status=%q", loaded.Agents, m.status)
	}
	view := m.viewAgentsLists()
	if !strings.Contains(view, "EXTERNAL ROLES") || !strings.Contains(view, "search") || !strings.Contains(view, "codex-main") {
		t.Fatalf("agents view = %q", view)
	}
}

func TestAgentsCustomCommandForm(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.agentsDraft = value
	m.agentsMode = agentsModeEditConnection
	fields := []string{"custom", "", "my-acp", `["stdio"]`, `{"TOKEN":"value"}`, ""}
	for index, field := range fields {
		m.agentsInputs[index].SetValue(field)
	}
	updated, _ := m.acceptAgentsForm()
	m = updated.(model)
	connection := m.agentsDraft.Agents.Connections["custom"]
	if connection.Command != "my-acp" || len(connection.Args) != 1 || connection.Env["TOKEN"] != "value" {
		t.Fatalf("connection = %#v status=%q", connection, m.status)
	}
}
