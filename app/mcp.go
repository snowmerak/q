package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/mcpconfig"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
)

type mcpScreenMode uint8

const (
	mcpModeList mcpScreenMode = iota
	mcpModeEditServer
)

type externalMCPConfigurer interface {
	ConfigureExternal(context.Context, string, mcpconfig.Config) []qtools.ExternalStatus
}

func (m model) enterMCP() (tea.Model, tea.Cmd) {
	value, err := m.mcpSettingsStore.LoadOrDefault()
	if err != nil {
		m.status = err.Error()
		return m, m.input.Focus()
	}
	m.screen = screenMCP
	m.input.Blur()
	m.mcpDraft = cloneMCPConfig(value)
	m.mcpOriginal = cloneMCPConfig(value)
	m.mcpPanel = 0
	m.mcpCursor = [2]int{}
	m.mcpMode = mcpModeList
	m.mcpEditID = ""
	m.mcpDiscardArmed = false
	m.mcpBusy = false
	m.status = ""
	m.resize(m.width, m.height)
	return m, nil
}

func (m model) updateMCP(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.mcpBusy {
		return m, nil
	}
	if m.mcpMode != mcpModeList {
		return m.updateMCPForm(key)
	}
	switch key.String() {
	case "tab", "left", "right":
		m.mcpPanel = 1 - m.mcpPanel
		m.status = ""
	case "up", "k":
		m.moveMCPCursor(-1)
	case "down", "j":
		m.moveMCPCursor(1)
	case "a":
		return m.beginMCPAdd()
	case "e", "enter":
		if m.mcpPanel == 1 {
			return m.beginMCPEdit()
		}
	case "d", "delete", "backspace":
		if m.mcpPanel == 1 {
			return m.deleteMCPServer()
		}
	case " ":
		return m.toggleMCPAssignment()
	case "esc":
		if m.mcpModified() && !m.mcpDiscardArmed {
			m.mcpDiscardArmed = true
			m.status = "Settings were not saved · press esc again to discard the pending changes"
			return m, nil
		}
		m.screen = screenChat
		m.status = ""
		if m.isStandaloneScreen(screenMCP) {
			return m, tea.Quit
		}
		return m, m.input.Focus()
	}
	return m, nil
}

func (m model) updateMCPForm(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.cancelMCPForm()
		return m, nil
	case "tab", "down":
		m.mcpFormFocus = (m.mcpFormFocus + 1) % len(m.mcpInputs)
		return m, m.focusMCPForm()
	case "shift+tab", "up":
		m.mcpFormFocus = (m.mcpFormFocus - 1 + len(m.mcpInputs)) % len(m.mcpInputs)
		return m, m.focusMCPForm()
	case "enter":
		return m.acceptMCPForm()
	}
	var command tea.Cmd
	m.mcpInputs[m.mcpFormFocus], command = m.mcpInputs[m.mcpFormFocus].Update(key)
	return m, command
}

func (m *model) moveMCPCursor(delta int) {
	length := len(m.mcpRoleIDs())
	if m.mcpPanel == 1 {
		length = len(m.mcpDraft.ServerIDs())
	}
	if length == 0 {
		m.mcpCursor[m.mcpPanel] = 0
		return
	}
	m.mcpCursor[m.mcpPanel] = (m.mcpCursor[m.mcpPanel] + delta + length) % length
}

func (m model) beginMCPAdd() (tea.Model, tea.Cmd) {
	for index := range m.mcpInputs {
		m.mcpInputs[index].Reset()
		m.mcpInputs[index].Blur()
	}
	m.mcpInputs[1].SetValue(mcpconfig.TransportStdio)
	m.mcpInputs[3].SetValue("[]")
	m.mcpInputs[4].SetValue("{}")
	m.mcpInputs[5].SetValue("{}")
	m.mcpMode = mcpModeEditServer
	m.mcpEditID = ""
	m.mcpFormFocus = 0
	m.status = ""
	return m, m.mcpInputs[0].Focus()
}

func (m model) beginMCPEdit() (tea.Model, tea.Cmd) {
	ids := m.mcpDraft.ServerIDs()
	if len(ids) == 0 {
		return m, nil
	}
	id := ids[min(m.mcpCursor[1], len(ids)-1)]
	server := m.mcpDraft.Servers[id]
	endpoint := server.Command
	if server.Transport == mcpconfig.TransportStreamableHTTP {
		endpoint = server.URL
	}
	args, _ := json.Marshal(server.Args)
	env, _ := json.Marshal(server.Env)
	headers, _ := json.Marshal(server.Headers)
	values := []string{id, server.Transport, endpoint, string(args), string(env), string(headers)}
	for index := range m.mcpInputs {
		m.mcpInputs[index].SetValue(values[index])
		m.mcpInputs[index].Blur()
	}
	m.mcpMode = mcpModeEditServer
	m.mcpEditID = id
	m.mcpFormFocus = 0
	m.status = ""
	return m, m.mcpInputs[0].Focus()
}

func (m model) acceptMCPForm() (tea.Model, tea.Cmd) {
	id := strings.TrimSpace(m.mcpInputs[0].Value())
	transport := strings.TrimSpace(m.mcpInputs[1].Value())
	endpoint := strings.TrimSpace(m.mcpInputs[2].Value())
	var args []string
	if err := decodeMCPJSON(m.mcpInputs[3].Value(), &args); err != nil {
		m.status = "Arguments must be a JSON string array · " + err.Error()
		return m, m.mcpInputs[3].Focus()
	}
	var env, headers map[string]string
	if err := decodeMCPJSON(m.mcpInputs[4].Value(), &env); err != nil {
		m.status = "Child env must be a JSON string map · " + err.Error()
		return m, m.mcpInputs[4].Focus()
	}
	if err := decodeMCPJSON(m.mcpInputs[5].Value(), &headers); err != nil {
		m.status = "Headers must be a JSON string map · " + err.Error()
		return m, m.mcpInputs[5].Focus()
	}
	server := mcpconfig.ServerConfig{Transport: transport}
	if transport == mcpconfig.TransportStdio {
		server.Command, server.Args, server.Env = endpoint, args, env
	} else {
		server.URL, server.Headers = endpoint, headers
	}
	candidate := cloneMCPConfig(m.mcpDraft)
	if id != m.mcpEditID {
		if _, exists := candidate.Servers[id]; exists {
			m.status = "MCP server profile already exists · " + id
			return m, m.mcpInputs[0].Focus()
		}
		delete(candidate.Servers, m.mcpEditID)
		for role, ids := range candidate.Roles {
			for index := range ids {
				if ids[index] == m.mcpEditID {
					ids[index] = id
				}
			}
			candidate.Roles[role] = ids
		}
	}
	candidate.Servers[id] = server
	if err := candidate.Validate(); err != nil {
		m.status = err.Error()
		return m, m.mcpInputs[m.mcpFormFocus].Focus()
	}
	m.mcpDraft = candidate
	m.mcpDiscardArmed = false
	m.cancelMCPForm()
	return m.saveMCPSettings()
}

func decodeMCPJSON(value string, output any) error {
	value = strings.TrimSpace(value)
	if value == "" {
		if object, ok := output.(*map[string]string); ok {
			*object = nil
			return nil
		}
		value = "[]"
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (m *model) cancelMCPForm() {
	for index := range m.mcpInputs {
		m.mcpInputs[index].Blur()
	}
	m.mcpMode = mcpModeList
	m.mcpEditID = ""
	m.mcpFormFocus = 0
}

func (m *model) focusMCPForm() tea.Cmd {
	for index := range m.mcpInputs {
		m.mcpInputs[index].Blur()
	}
	return m.mcpInputs[m.mcpFormFocus].Focus()
}

func (m model) deleteMCPServer() (tea.Model, tea.Cmd) {
	ids := m.mcpDraft.ServerIDs()
	if len(ids) == 0 {
		return m, nil
	}
	id := ids[min(m.mcpCursor[1], len(ids)-1)]
	delete(m.mcpDraft.Servers, id)
	for role, assigned := range m.mcpDraft.Roles {
		m.mcpDraft.Roles[role] = removeMCPID(assigned, id)
		if len(m.mcpDraft.Roles[role]) == 0 {
			delete(m.mcpDraft.Roles, role)
		}
	}
	m.mcpCursor[1] = min(m.mcpCursor[1], max(0, len(ids)-2))
	m.mcpDiscardArmed = false
	return m.saveMCPSettings()
}

func (m model) toggleMCPAssignment() (tea.Model, tea.Cmd) {
	roles, servers := m.mcpRoleIDs(), m.mcpDraft.ServerIDs()
	if len(roles) == 0 || len(servers) == 0 {
		m.status = "Add an MCP server first"
		return m, nil
	}
	role := roles[min(m.mcpCursor[0], len(roles)-1)]
	server := servers[min(m.mcpCursor[1], len(servers)-1)]
	assigned := append([]string(nil), m.mcpDraft.Roles[role]...)
	if containsMCPID(assigned, server) {
		assigned = removeMCPID(assigned, server)
	} else {
		assigned = append(assigned, server)
		sort.Strings(assigned)
	}
	if len(assigned) == 0 {
		delete(m.mcpDraft.Roles, role)
	} else {
		m.mcpDraft.Roles[role] = assigned
	}
	m.mcpDiscardArmed = false
	return m.saveMCPSettings()
}

func (m model) saveMCPSettings() (tea.Model, tea.Cmd) {
	if err := m.mcpDraft.Validate(); err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.mcpBusy = true
	m.status = "Saving MCP settings…"
	value := cloneMCPConfig(m.mcpDraft)
	store := m.mcpSettingsStore
	ctx := m.ctx
	root := ""
	if m.workspaceStore != nil {
		root = m.workspaceStore.Root
	}
	configurer, _ := m.toolRuntime.(externalMCPConfigurer)
	return m, func() tea.Msg {
		if err := store.Save(value); err != nil {
			return mcpSettingsSavedMsg{err: err}
		}
		var statuses []qtools.ExternalStatus
		if configurer != nil {
			statuses = configurer.ConfigureExternal(ctx, root, value)
		}
		return mcpSettingsSavedMsg{config: value, statuses: statuses}
	}
}

func renderMCPSaveStatus(statuses []qtools.ExternalStatus, standalone bool) string {
	if standalone {
		return "MCP settings saved · restart q to activate changes"
	}
	connected, toolCount := 0, 0
	var failures []string
	for _, status := range statuses {
		if status.Error == "" {
			connected++
			toolCount += status.Tools
		} else {
			failures = append(failures, status.ID+": "+status.Error)
		}
	}
	result := fmt.Sprintf("MCP settings applied · %d server(s) · %d tool(s)", connected, toolCount)
	if len(failures) > 0 {
		result += " · " + strings.Join(failures, " · ")
	}
	return result
}

func (m model) viewMCP() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · External MCP"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render("settings · " + m.mcpSettingsStore.Path() + " · credentials are environment-variable references"))
	body.WriteString("\n\n")
	if m.mcpMode == mcpModeEditServer {
		body.WriteString(m.viewMCPForm())
	} else {
		body.WriteString(m.viewMCPLists())
	}
	if m.status != "" {
		body.WriteString("\n\n")
		body.WriteString(subtleStyle.Render(m.status))
	}
	body.WriteString("\n\n")
	help := "tab/←/→ panel · ↑/↓ select · space assign · a add · e/enter edit · d delete · esc chat"
	if m.mcpMode != mcpModeList {
		help = "tab/↑/↓ field · enter apply · esc cancel"
	} else if m.isStandaloneScreen(screenMCP) {
		help = strings.TrimSuffix(help, "esc chat") + "esc quit"
	}
	body.WriteString(helpStyle.Render(help))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewMCPLists() string {
	width, gap := max(36, m.width-4), 2
	panelWidth := max(16, (width-gap)/2)
	roleTitle, serverTitle := "ROLES", "MCP SERVERS"
	if m.mcpPanel == 0 {
		roleTitle = "› " + roleTitle
	} else {
		serverTitle = "› " + serverTitle
	}
	roles, servers := m.mcpRoleIDs(), m.mcpDraft.ServerIDs()
	selectedRole := ""
	if len(roles) > 0 {
		selectedRole = roles[min(m.mcpCursor[0], len(roles)-1)]
	}
	var left strings.Builder
	left.WriteString(agentTraceTitleStyle(m.dark).Render(roleTitle))
	left.WriteString("\n")
	start, end := lspVisibleRange(len(roles), m.mcpCursor[0], max(2, m.height-14))
	for index := start; index < end; index++ {
		cursor := "  "
		if m.mcpPanel == 0 && index == m.mcpCursor[0] {
			cursor = "› "
		} else if index == m.mcpCursor[0] {
			cursor = "• "
		}
		left.WriteString(fmt.Sprintf("%s%s\n", cursor, mcpRoleLabel(roles[index])))
		left.WriteString(subtleStyle.Render(fmt.Sprintf("    %d server(s)\n", len(m.mcpDraft.Roles[roles[index]]))))
	}
	var right strings.Builder
	right.WriteString(agentTraceTitleStyle(m.dark).Render(serverTitle))
	right.WriteString("\n")
	if len(servers) == 0 {
		right.WriteString(subtleStyle.Render("  No servers · press a"))
	}
	start, end = lspVisibleRange(len(servers), m.mcpCursor[1], max(2, m.height-14))
	for index := start; index < end; index++ {
		id := servers[index]
		cursor := "  "
		if m.mcpPanel == 1 && index == m.mcpCursor[1] {
			cursor = "› "
		}
		mark := " "
		if containsMCPID(m.mcpDraft.Roles[selectedRole], id) {
			mark = "x"
		}
		server := m.mcpDraft.Servers[id]
		right.WriteString(fmt.Sprintf("%s[%s] %s\n", cursor, mark, id))
		endpoint := server.Command
		if server.Transport == mcpconfig.TransportStreamableHTTP {
			endpoint = server.URL
		}
		right.WriteString(subtleStyle.Render("    " + server.Transport + " · " + endpoint + "\n"))
	}
	panel := lipgloss.NewStyle().Width(panelWidth)
	return lipgloss.JoinHorizontal(lipgloss.Top, panel.Render(left.String()), strings.Repeat(" ", gap), panel.Render(right.String()))
}

func (m model) viewMCPForm() string {
	labels := []string{"Server ID", "Transport", "Command or URL", "Arguments (JSON array)", "Child env: target -> source env (JSON object)", "HTTP header -> source env (JSON object)"}
	var body strings.Builder
	body.WriteString(agentTraceTitleStyle(m.dark).Render("MCP SERVER PROFILE"))
	body.WriteString("\n")
	for index, label := range labels {
		prefix := "  "
		if index == m.mcpFormFocus {
			prefix = "› "
		}
		body.WriteString(activeLabelStyle.Render(prefix + label))
		body.WriteString("\n  ")
		body.WriteString(m.mcpInputs[index].View())
		body.WriteString("\n")
	}
	return body.String()
}

func (m model) mcpRoleIDs() []string {
	return mcpconfig.RoleIDs()
}

func mcpRoleLabel(role string) string {
	if role == mcpconfig.RoleDefault {
		return role + " (main chat)"
	}
	return role
}

func (m model) mcpModified() bool {
	left, _ := json.Marshal(m.mcpDraft)
	right, _ := json.Marshal(m.mcpOriginal)
	return string(left) != string(right)
}

func cloneMCPConfig(value mcpconfig.Config) mcpconfig.Config {
	result := mcpconfig.Config{Version: value.Version, Servers: make(map[string]mcpconfig.ServerConfig, len(value.Servers)), Roles: make(map[string][]string, len(value.Roles))}
	for id, server := range value.Servers {
		server.Args = append([]string(nil), server.Args...)
		server.Env = cloneStringMap(server.Env)
		server.Headers = cloneStringMap(server.Headers)
		server.ResolvedEnv = cloneStringMap(server.ResolvedEnv)
		server.ResolvedHeaders = cloneStringMap(server.ResolvedHeaders)
		result.Servers[id] = server
	}
	for role, servers := range value.Roles {
		result.Roles[role] = append([]string(nil), servers...)
	}
	return result
}

func cloneStringMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func containsMCPID(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func removeMCPID(values []string, target string) []string {
	result := values[:0]
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

// RunMCP opens role-to-server assignment settings. It does not start external
// MCP processes itself.
func RunMCP(ctx context.Context, store config.Store, workspaceStore workspace.Store) error {
	m := newModel(ctx, store, nil)
	m.workspaceStore = &workspaceStore
	updated, _ := m.enterMCP()
	m = updated.(model)
	if m.screen != screenMCP {
		return errors.New(m.status)
	}
	_, err := runStandalone(m, screenMCP)
	return err
}

func RunMCPDefault(ctx context.Context) error {
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	workspaceStore, err := workspace.DefaultStore()
	if err != nil {
		return err
	}
	if err := RunMCP(ctx, store, workspaceStore); err != nil {
		return fmt.Errorf("q mcp: %w", err)
	}
	return nil
}
