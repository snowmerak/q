package app

import (
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/snowmerak/q/workspace"
)

func (m model) enterIgnore() (tea.Model, tea.Cmd) {
	if m.workspaceStore == nil {
		m.status = "Workspace storage is unavailable"
		return m, m.input.Focus()
	}
	content, err := m.workspaceStore.LoadIgnore()
	if err != nil {
		m.status = err.Error()
		return m, m.input.Focus()
	}
	m.screen = screenIgnore
	m.input.Blur()
	m.ignoreEditor.SetValue(content)
	m.ignoreOriginal = content
	m.ignoreDiscardArmed = false
	m.status = ""
	m.resize(m.width, m.height)
	return m, m.ignoreEditor.Focus()
}

func (m model) updateIgnore(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+s":
		return m.saveIgnore()
	case "esc":
		if m.ignoreEditor.Value() != m.ignoreOriginal && !m.ignoreDiscardArmed {
			m.ignoreDiscardArmed = true
			m.status = "Unsaved changes · press esc again to discard or ctrl+s to save"
			return m, nil
		}
		m.screen = screenChat
		m.ignoreEditor.Blur()
		m.ignoreDiscardArmed = false
		m.status = ""
		if m.isStandaloneScreen(screenIgnore) {
			return m, tea.Quit
		}
		return m, m.input.Focus()
	}
	before := m.ignoreEditor.Value()
	var command tea.Cmd
	m.ignoreEditor, command = m.ignoreEditor.Update(key)
	if m.ignoreEditor.Value() != before {
		m.ignoreDiscardArmed = false
		m.status = ""
	}
	return m, command
}

func (m model) saveIgnore() (tea.Model, tea.Cmd) {
	if m.workspaceStore == nil {
		m.status = "Workspace storage is unavailable"
		return m, nil
	}
	if err := m.workspaceStore.SaveIgnore(m.ignoreEditor.Value()); err != nil {
		m.status = err.Error()
		return m, nil
	}
	content, err := m.workspaceStore.LoadIgnore()
	if err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.ignoreEditor.SetValue(content)
	m.ignoreOriginal = content
	m.ignoreDiscardArmed = false
	m.status = workspace.IgnoreFileName + " saved · new directory listings use it immediately"
	return m, m.ignoreEditor.Focus()
}

func (m model) viewIgnore() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · Discovery ignore"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	path := workspace.IgnoreFileName
	if m.workspaceStore != nil {
		path = filepath.Clean(m.workspaceStore.IgnorePath())
	}
	body.WriteString(subtleStyle.Render("file · " + path))
	body.WriteString("\n")
	body.WriteString(subtleStyle.Render("built-in · root .q/ is always excluded"))
	body.WriteString("\n")
	body.WriteString(subtleStyle.Render("syntax · # comment · ! include · / root · trailing / directory · * ** ? wildcards"))
	body.WriteString("\n\n")
	label := "DISCOVERY PATTERNS"
	if m.ignoreEditor.Value() != m.ignoreOriginal {
		label += " · modified"
	}
	panel := ignoreEditorPanelStyle(max(36, m.width-4), m.dark)
	body.WriteString(agentTraceTitleStyle(m.dark).Render(label))
	body.WriteString("\n")
	body.WriteString(panel.Render(m.ignoreEditor.View()))
	if m.status != "" {
		body.WriteString("\n\n")
		style := subtleStyle
		if strings.Contains(strings.ToLower(m.status), "unavailable") || strings.Contains(strings.ToLower(m.status), "workspace:") {
			style = errorStyle
		}
		body.WriteString(style.Render(m.status))
	}
	body.WriteString("\n\n")
	help := "ctrl+s save · enter newline · esc chat (press twice to discard changes) · ctrl+c quit"
	if m.isStandaloneScreen(screenIgnore) {
		help = "ctrl+s save · enter newline · esc quit (press twice to discard changes) · ctrl+c quit"
	}
	body.WriteString(helpStyle.Render(help))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func ignoreEditorPanelStyle(width int, dark bool) lipgloss.Style {
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.Border{Left: "│"}).BorderLeft(true).
		BorderForeground(themedColor(dark, "99", "61")).
		PaddingLeft(1).Width(max(10, width-4))
}
