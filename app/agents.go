package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/snowmerak/q/config"
)

type agentsScreenMode uint8

const (
	agentsModeList agentsScreenMode = iota
	agentsModeEditConnection
)

func (m model) enterAgents() (tea.Model, tea.Cmd) {
	value, err := m.store.Load()
	if err != nil {
		m.status = err.Error()
		return m, m.input.Focus()
	}
	m.screen = screenAgents
	m.input.Blur()
	m.agentsDraft = cloneConfigForAgents(value)
	m.agentsOriginal = cloneConfigForAgents(value)
	m.agentsPanel = 0
	m.agentsCursor = [2]int{}
	m.agentsMode = agentsModeList
	m.agentsEditID = ""
	m.agentsDiscardArmed = false
	m.agentsBusy = false
	m.agentsProbe = make(map[string]string)
	m.status = ""
	m.resize(m.width, m.height)
	return m, nil
}

func (m model) updateAgents(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.agentsBusy {
		return m, nil
	}
	if m.agentsMode != agentsModeList {
		return m.updateAgentsForm(key)
	}
	switch key.String() {
	case "tab", "left", "right":
		m.agentsPanel = 1 - m.agentsPanel
		m.status = ""
	case "up", "k":
		m.moveAgentsCursor(-1)
	case "down", "j":
		m.moveAgentsCursor(1)
	case "a":
		return m.beginAgentConnectionAdd()
	case "e", "enter":
		if m.agentsPanel == 1 {
			return m.beginAgentConnectionEdit()
		}
	case "d", "delete", "backspace":
		if m.agentsPanel == 1 {
			return m.deleteAgentConnection()
		}
	case " ":
		return m.assignAgentRole()
	case "t":
		if m.agentsPanel == 1 {
			return m.toggleAgentConnection()
		}
	case "c":
		return m.probeAgentConnection()
	case "ctrl+s":
		return m.saveAgentsSettings()
	case "esc":
		if m.agentsModified() && !m.agentsDiscardArmed {
			m.agentsDiscardArmed = true
			m.status = "Unsaved changes · press esc again to discard or ctrl+s to save"
			return m, nil
		}
		m.screen = screenChat
		m.status = ""
		if m.isStandaloneScreen(screenAgents) {
			return m, tea.Quit
		}
		return m, m.input.Focus()
	}
	return m, nil
}

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
	case "ctrl+s", "enter":
		return m.acceptAgentsForm()
	}
	var command tea.Cmd
	m.agentsInputs[m.agentsFormFocus], command = m.agentsInputs[m.agentsFormFocus].Update(key)
	return m, command
}

func (m *model) moveAgentsCursor(delta int) {
	length := len(agentExternalRoles())
	if m.agentsPanel == 1 {
		length = len(agentConnectionIDs(m.agentsDraft.Agents))
	}
	if length == 0 {
		m.agentsCursor[m.agentsPanel] = 0
		return
	}
	m.agentsCursor[m.agentsPanel] = (m.agentsCursor[m.agentsPanel] + delta + length) % length
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
	candidate := cloneConfigForAgents(m.agentsDraft)
	if candidate.Agents.Connections == nil {
		candidate.Agents.Connections = make(map[string]config.AgentConnectionConfig)
	}
	if id != m.agentsEditID {
		if _, exists := candidate.Agents.Connections[id]; exists {
			m.status = "Agent connection already exists · " + id
			return m, m.agentsInputs[0].Focus()
		}
		delete(candidate.Agents.Connections, m.agentsEditID)
		for role, assignment := range candidate.Agents.Roles {
			if assignment.Agent == m.agentsEditID {
				assignment.Agent = id
				candidate.Agents.Roles[role] = assignment
			}
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
	m.agentsDiscardArmed = false
	m.cancelAgentsForm()
	m.status = "Draft updated · ctrl+s to save"
	return m, nil
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
	delete(m.agentsDraft.Agents.Connections, id)
	delete(m.agentsProbe, id)
	for role, assignment := range m.agentsDraft.Agents.Roles {
		if assignment.Agent == id {
			assignment.Agent = ""
			m.agentsDraft.Agents.Roles[role] = assignment
		}
	}
	m.agentsCursor[1] = min(m.agentsCursor[1], max(0, len(ids)-2))
	m.agentsDiscardArmed = false
	m.status = "Connection removed from draft · ctrl+s to save"
	return m, nil
}

func (m model) assignAgentRole() (tea.Model, tea.Cmd) {
	roles, ids := agentExternalRoles(), agentConnectionIDs(m.agentsDraft.Agents)
	if len(ids) == 0 {
		m.status = "Add an ACP agent connection first"
		return m, nil
	}
	role := roles[min(m.agentsCursor[0], len(roles)-1)]
	id := ids[min(m.agentsCursor[1], len(ids)-1)]
	assignment := m.agentsDraft.Agents.Roles[role]
	if assignment.Agent == id {
		assignment.Agent = ""
	} else {
		assignment.Agent = id
	}
	if m.agentsDraft.Agents.Roles == nil {
		m.agentsDraft.Agents.Roles = make(map[string]config.AgentConfig)
	}
	m.agentsDraft.Agents.Roles[role] = assignment
	m.agentsDiscardArmed = false
	m.status = "Role assignment draft updated · ctrl+s to save"
	return m, nil
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
	m.agentsDiscardArmed = false
	state := "enabled"
	if connection.Disabled {
		state = "disabled"
	}
	m.status = id + " " + state + " in draft · ctrl+s to save"
	return m, nil
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

func (m model) viewAgents() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · Agents"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render("ACP connections and external role assignments · " + m.store.Path()))
	body.WriteString("\n\n")
	if m.agentsMode == agentsModeEditConnection {
		body.WriteString(m.viewAgentsForm())
	} else {
		body.WriteString(m.viewAgentsLists())
	}
	if m.status != "" {
		body.WriteString("\n\n")
		body.WriteString(subtleStyle.Render(m.status))
	}
	body.WriteString("\n\n")
	help := "tab/←/→ panel · ↑/↓ select · space assign · c test · a add · e/enter edit · t enable/disable · d delete · ctrl+s save · esc chat"
	if m.agentsMode != agentsModeList {
		help = "tab/↑/↓ field · enter/ctrl+s apply draft · esc cancel"
	} else if m.isStandaloneScreen(screenAgents) {
		help = strings.TrimSuffix(help, "esc chat") + "esc quit"
	}
	body.WriteString(helpStyle.Render(help))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewAgentsLists() string {
	width, gap := max(36, m.width-4), 2
	panelWidth := max(16, (width-gap)/2)
	roleTitle, connectionTitle := "EXTERNAL ROLES", "ACP AGENTS"
	if m.agentsPanel == 0 {
		roleTitle = "› " + roleTitle
	} else {
		connectionTitle = "› " + connectionTitle
	}
	roles, ids := agentExternalRoles(), agentConnectionIDs(m.agentsDraft.Agents)
	selectedRole := roles[min(m.agentsCursor[0], len(roles)-1)]
	assigned := m.agentsDraft.Agents.Roles[selectedRole].Agent
	var left strings.Builder
	left.WriteString(agentTraceTitleStyle(m.dark).Render(roleTitle))
	left.WriteString("\n")
	for index, role := range roles {
		cursor := "  "
		if m.agentsPanel == 0 && index == m.agentsCursor[0] {
			cursor = "› "
		}
		left.WriteString(cursor + role + "\n")
		connection := m.agentsDraft.Agents.Roles[role].Agent
		if connection == "" {
			connection = "not assigned"
		}
		left.WriteString(subtleStyle.Render("    " + connection + "\n"))
	}
	var right strings.Builder
	right.WriteString(agentTraceTitleStyle(m.dark).Render(connectionTitle))
	right.WriteString("\n")
	if len(ids) == 0 {
		right.WriteString(subtleStyle.Render("  No connections · press a"))
	}
	start, end := lspVisibleRange(len(ids), m.agentsCursor[1], max(2, m.height-14))
	for index := start; index < end; index++ {
		id := ids[index]
		cursor := "  "
		if m.agentsPanel == 1 && index == m.agentsCursor[1] {
			cursor = "› "
		}
		mark := " "
		if id == assigned {
			mark = "x"
		}
		connection := m.agentsDraft.Agents.Connections[id]
		state := "enabled"
		if connection.Disabled {
			state = "disabled"
		}
		if probe := m.agentsProbe[id]; probe != "" {
			state += " · " + probe
		}
		right.WriteString(fmt.Sprintf("%s[%s] %s\n", cursor, mark, id))
		right.WriteString(subtleStyle.Render("    " + agentConnectionEndpoint(connection) + " · " + state + "\n"))
	}
	panel := lipgloss.NewStyle().Width(panelWidth)
	return lipgloss.JoinHorizontal(lipgloss.Top, panel.Render(left.String()), strings.Repeat(" ", gap), panel.Render(right.String()))
}

func (m model) viewAgentsForm() string {
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

func agentExternalRoles() []string { return []string{config.AgentRoleSearch} }

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

func (m model) agentsModified() bool {
	left, _ := json.Marshal(m.agentsDraft.Agents)
	right, _ := json.Marshal(m.agentsOriginal.Agents)
	return string(left) != string(right)
}

func cloneConfigForAgents(value config.Config) config.Config {
	result := value
	result.Agents.Roles = make(map[string]config.AgentConfig, len(value.Agents.Roles))
	for role, assignment := range value.Agents.Roles {
		result.Agents.Roles[role] = assignment
	}
	result.Agents.Connections = make(map[string]config.AgentConnectionConfig, len(value.Agents.Connections))
	for id, connection := range value.Agents.Connections {
		connection.Args = append([]string(nil), connection.Args...)
		connection.Env = cloneStringMap(connection.Env)
		result.Agents.Connections[id] = connection
	}
	return result
}

// RunAgents opens ACP connection and external role assignment settings.
func RunAgents(ctx context.Context, store config.Store) error {
	m := newModel(ctx, store, nil)
	updated, _ := m.enterAgents()
	m = updated.(model)
	if m.screen != screenAgents {
		return errors.New(m.status)
	}
	_, err := runStandalone(m, screenAgents)
	return err
}

func RunAgentsDefault(ctx context.Context) error {
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	if err := RunAgents(ctx, store); err != nil {
		return fmt.Errorf("q agents: %w", err)
	}
	return nil
}
