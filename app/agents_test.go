package app

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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

func TestAgentsAddingConnectionDoesNotAssignNativeRoles(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	value.Agents.Roles = map[string]config.AgentConfig{
		config.AgentRolePlanner: {Model: "planner-model"},
		config.AgentRoleGriller: {Model: "griller-model"},
	}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.agentsDraft = value
	m.agentsMode = agentsModeEditConnection
	fields := []string{"grok", "grok", "", `[]`, `{}`, ""}
	for index, field := range fields {
		m.agentsInputs[index].SetValue(field)
	}
	updated, _ := m.acceptAgentsForm()
	m = updated.(model)
	if m.agentsDraft.Agents.Connections["grok"].Preset != "grok" {
		t.Fatalf("connection = %#v status=%q", m.agentsDraft.Agents.Connections["grok"], m.status)
	}
	for _, role := range []string{config.AgentRolePlanner, config.AgentRoleGriller} {
		assignment := m.agentsDraft.Agents.Roles[role]
		if assignment.Agent != "" {
			t.Fatalf("native role %q was assigned ACP connection %#v", role, assignment)
		}
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

	updated, _ = m.Update(agentConnectionProbedMsg{id: "grok", err: context.DeadlineExceeded})
	m = updated.(model)
	if m.agentsProbe["grok"] != "failed" || !strings.Contains(m.status, "deadline exceeded") {
		t.Fatalf("probe=%q status=%q", m.agentsProbe["grok"], m.status)
	}
}

func TestAgentsSpaceKeyAssignsSelectedConnection(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.agentsDraft = config.Default()
	m.agentsDraft.Agents.Connections = map[string]config.AgentConnectionConfig{
		"codex": {Preset: "codex"},
	}
	m.agentsMode = agentsModeList
	updated, _ := m.updateAgents(tea.KeyPressMsg{Code: tea.KeySpace})
	m = updated.(model)
	if assigned := m.agentsDraft.Agents.Roles[config.AgentRoleSearch].Agent; assigned != "codex" {
		t.Fatalf("search role assignment = %q", assigned)
	}
}

func TestAgentsConnectionRowsStayColumnAligned(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.width, m.height = 160, 30
	m.agentsDraft = config.Default()
	m.agentsDraft.Agents.Connections = map[string]config.AgentConnectionConfig{
		"codex": {Preset: "codex"},
		"grok":  {Preset: "grok"},
	}
	m.agentsPanel = 1
	plain := ansi.Strip(m.viewAgentsLists())
	columns := make(map[string]int)
	for _, line := range strings.Split(plain, "\n") {
		for _, id := range []string{"codex", "grok"} {
			if column := strings.Index(line, "[ ] "+id); column >= 0 {
				columns[id] = ansi.StringWidth(line[:column])
			}
		}
	}
	if columns["codex"] != columns["grok"] {
		t.Fatalf("connection columns = %#v\n%s", columns, plain)
	}
}
