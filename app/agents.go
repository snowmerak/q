package app

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
)

type agentsScreenMode uint8

const (
	agentsModeList agentsScreenMode = iota
	agentsModeEditConnection
)

func (m model) updateAgentsForm(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.cancelAgentsForm()
		return m, nil
	case "tab", "down":
		m.agentsFormFocus = (m.agentsFormFocus + 1) % len(m.agentsInputs)
		return m, m.focusAgentsForm()
	case "shift+tab", "up":
		m.agentsFormFocus = (m.agentsFormFocus - 1 + len(m.agentsInputs)) % len(m.agentsInputs)
		return m, m.focusAgentsForm()
	case "enter":
		return m.acceptAgentsForm()
	}
	var command tea.Cmd
	m.agentsInputs[m.agentsFormFocus], command = m.agentsInputs[m.agentsFormFocus].Update(key)
	return m, command
}

func (m model) beginAgentConnectionAdd() (tea.Model, tea.Cmd) {
	for index := range m.agentsInputs {
		m.agentsInputs[index].Reset()
		m.agentsInputs[index].Blur()
	}
	m.agentsInputs[1].SetValue("codex")
	m.agentsInputs[3].SetValue("[]")
	m.agentsInputs[4].SetValue("{}")
	m.agentsMode = agentsModeEditConnection
	m.agentsEditID = ""
	m.agentsFormFocus = 0
	m.status = ""
	return m, m.agentsInputs[0].Focus()
}

func (m model) beginAgentConnectionEdit() (tea.Model, tea.Cmd) {
	ids := agentConnectionIDs(m.agentsDraft.Agents)
	if len(ids) == 0 {
		return m, nil
	}
	id := ids[min(m.agentsCursor[1], len(ids)-1)]
	connection := m.agentsDraft.Agents.Connections[id]
	args, _ := json.Marshal(connection.Args)
	env, _ := json.Marshal(connection.Env)
	values := []string{id, connection.Preset, connection.Command, string(args), string(env), connection.AuthMethod}
	for index := range m.agentsInputs {
		m.agentsInputs[index].SetValue(values[index])
		m.agentsInputs[index].Blur()
	}
	m.agentsMode = agentsModeEditConnection
	m.agentsEditID = id
	m.agentsFormFocus = 0
	m.status = ""
	return m, m.agentsInputs[0].Focus()
}

func (m model) acceptAgentsForm() (tea.Model, tea.Cmd) {
	id := strings.TrimSpace(m.agentsInputs[0].Value())
	preset := strings.TrimSpace(m.agentsInputs[1].Value())
	command := strings.TrimSpace(m.agentsInputs[2].Value())
	var args []string
	if err := decodeMCPJSON(m.agentsInputs[3].Value(), &args); err != nil {
		m.status = "Arguments must be a JSON string array · " + err.Error()
		return m, m.agentsInputs[3].Focus()
	}
	var env map[string]string
	if err := decodeMCPJSON(m.agentsInputs[4].Value(), &env); err != nil {
		m.status = "Environment must be a JSON string map · " + err.Error()
		return m, m.agentsInputs[4].Focus()
	}
	connection := config.AgentConnectionConfig{
		Preset: preset, Command: command, Args: args, Env: env,
		AuthMethod: strings.TrimSpace(m.agentsInputs[5].Value()),
	}
	if previous, found := m.agentsDraft.Agents.Connections[m.agentsEditID]; found {
		connection.Disabled = previous.Disabled
	}
	if m.agentsEditID != "" && id != m.agentsEditID {
		m.status = "Connection ID cannot change; add a new connection instead"
		return m, m.agentsInputs[0].Focus()
	}
	candidate := cloneConfigForAgents(m.agentsDraft)
	if candidate.Agents.Connections == nil {
		candidate.Agents.Connections = make(map[string]config.AgentConnectionConfig)
	}
	if m.agentsEditID == "" {
		if _, exists := candidate.Agents.Connections[id]; exists {
			m.status = "Agent connection already exists · " + id
			return m, m.agentsInputs[0].Focus()
		}
	}
	candidate.Agents.Connections[id] = connection
	if err := candidate.Validate(); err != nil {
		m.status = err.Error()
		return m, m.agentsInputs[m.agentsFormFocus].Focus()
	}
	delete(m.agentsProbe, m.agentsEditID)
	delete(m.agentsProbe, id)
	m.agentsDraft = candidate
	m.cancelAgentsForm()
	return m.saveAgentsSettings()
}

func (m *model) cancelAgentsForm() {
	for index := range m.agentsInputs {
		m.agentsInputs[index].Blur()
	}
	m.agentsMode = agentsModeList
	m.agentsEditID = ""
	m.agentsFormFocus = 0
}

func (m *model) focusAgentsForm() tea.Cmd {
	for index := range m.agentsInputs {
		m.agentsInputs[index].Blur()
	}
	return m.agentsInputs[m.agentsFormFocus].Focus()
}

func (m model) deleteAgentConnection() (tea.Model, tea.Cmd) {
	ids := agentConnectionIDs(m.agentsDraft.Agents)
	if len(ids) == 0 {
		return m, nil
	}
	id := ids[min(m.agentsCursor[1], len(ids)-1)]
	var references []string
	for _, definition := range subagent.ExternalAgentDefinitions() {
		if m.agentsDraft.Agents.Roles[definition.Info.Role].Agent == id {
			references = append(references, definition.Info.Name)
		}
	}
	for _, entry := range m.customStore().List() {
		if entry.Err == nil && entry.Profile.EffectiveKind() == subagent.AgentKindExternal && entry.Profile.Agent == id {
			references = append(references, subagent.CanonicalProfileID(entry.Scope, entry.Profile.Name))
		}
	}
	if len(references) > 0 {
		sort.Strings(references)
		m.status = "Connection is used by subagents: " + strings.Join(references, ", ")
		return m, nil
	}
	delete(m.agentsDraft.Agents.Connections, id)
	delete(m.agentsProbe, id)
	m.agentsCursor[1] = min(m.agentsCursor[1], max(0, len(ids)-2))
	return m.saveAgentsSettings()
}

func (m model) toggleAgentConnection() (tea.Model, tea.Cmd) {
	ids := agentConnectionIDs(m.agentsDraft.Agents)
	if len(ids) == 0 {
		return m, nil
	}
	id := ids[min(m.agentsCursor[1], len(ids)-1)]
	connection := m.agentsDraft.Agents.Connections[id]
	connection.Disabled = !connection.Disabled
	m.agentsDraft.Agents.Connections[id] = connection
	return m.saveAgentsSettings()
}

func (m model) probeAgentConnection() (tea.Model, tea.Cmd) {
	ids := agentConnectionIDs(m.agentsDraft.Agents)
	if len(ids) == 0 {
		m.status = "Add an ACP agent connection first"
		return m, nil
	}
	id := ids[min(m.agentsCursor[1], len(ids)-1)]
	connection := m.agentsDraft.Agents.Connections[id]
	root, err := os.Getwd()
	if err == nil {
		root, err = canonicalWorkspaceRoot(root)
	}
	if err != nil {
		m.status = "Resolve workspace for connection test · " + err.Error()
		return m, nil
	}
	if m.agentsProbe == nil {
		m.agentsProbe = make(map[string]string)
	}
	m.agentsProbe[id] = "testing"
	m.agentsBusy = true
	m.status = "Testing " + id + " · initialize/session lifecycle…"
	parent := m.ctx
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 45*time.Second)
		defer cancel()
		return agentConnectionProbedMsg{
			id: id, err: probeACPAgentConnection(ctx, root, id, connection),
		}
	}
}

func (m model) saveAgentsSettings() (tea.Model, tea.Cmd) {
	if err := m.agentsDraft.Validate(); err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.agentsBusy = true
	m.status = "Saving agent settings…"
	value := cloneConfigForAgents(m.agentsDraft)
	store := m.store
	return m, func() tea.Msg {
		if err := store.Save(value); err != nil {
			return agentsSettingsSavedMsg{err: err}
		}
		return agentsSettingsSavedMsg{config: value}
	}
}

func (m model) viewACPConnectionForm() string {
	labels := []string{"Connection ID", "Preset (codex/grok; empty for custom)", "Custom command", "Arguments (JSON array)", "Child environment (JSON object)", "ACP auth method (optional)"}
	var body strings.Builder
	body.WriteString(agentTraceTitleStyle(m.dark).Render("ACP AGENT CONNECTION"))
	body.WriteString("\n")
	for index, label := range labels {
		prefix := "  "
		if index == m.agentsFormFocus {
			prefix = "› "
		}
		body.WriteString(activeLabelStyle.Render(prefix + label))
		body.WriteString("\n  ")
		body.WriteString(m.agentsInputs[index].View())
		body.WriteString("\n")
	}
	return body.String()
}

func agentConnectionIDs(value config.AgentsConfig) []string {
	ids := make([]string, 0, len(value.Connections))
	for id := range value.Connections {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func agentConnectionEndpoint(connection config.AgentConnectionConfig) string {
	if connection.Preset != "" {
		return "preset " + connection.Preset
	}
	return strings.Join(append([]string{connection.Command}, connection.Args...), " ")
}

func cloneConfigForAgents(value config.Config) config.Config {
	result := value
	result.Agents.Roles = make(map[string]config.AgentConfig, len(value.Agents.Roles))
	maps.Copy(result.Agents.Roles, value.Agents.Roles)
	result.Agents.Connections = make(map[string]config.AgentConnectionConfig, len(value.Agents.Connections))
	for id, connection := range value.Agents.Connections {
		connection.Args = append([]string(nil), connection.Args...)
		connection.Env = cloneStringMap(connection.Env)
		result.Agents.Connections[id] = connection
	}
	return result
}
