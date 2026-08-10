package app

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func (m model) enterHelp() (tea.Model, tea.Cmd) {
	if m.screen != screenHelp {
		m.helpReturn = m.screen
	}
	m.screen = screenHelp
	m.input.Blur()
	m.ignoreEditor.Blur()
	m.skillsInput.Blur()
	m.modelFilter.Blur()
	m.embeddingDimensions.Blur()
	m.modelContextWindow.Blur()
	for index := range m.setup {
		m.setup[index].Blur()
	}
	for index := range m.loomInputs {
		m.loomInputs[index].Blur()
	}
	m.resize(m.width, m.height)
	m.refreshHelp(true)
	return m, nil
}

func (m model) leaveHelp() (tea.Model, tea.Cmd) {
	returnScreen := m.helpReturn
	if returnScreen == screenHelp || returnScreen > screenChat {
		returnScreen = screenChat
	}
	m.screen = returnScreen
	m.resize(m.width, m.height)
	switch returnScreen {
	case screenSetup:
		return m, m.setup[m.setupFocus].Focus()
	case screenModels:
		return m, m.modelPickerFocus()
	case screenLoom:
		return m, m.loomFocusCommand()
	case screenIgnore:
		return m, m.ignoreEditor.Focus()
	case screenSkills:
		if m.skillsMode == skillModeAdd {
			return m, m.skillsInput.Focus()
		}
		return m, nil
	case screenChat:
		m.refreshTranscript()
		m.refreshAgentTrace(false)
		if !m.waiting || m.asking {
			return m, m.input.Focus()
		}
	}
	return m, nil
}

func (m model) updateHelp(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		return m.leaveHelp()
	case "home":
		m.helpViewport.GotoTop()
		return m, nil
	case "end":
		m.helpViewport.GotoBottom()
		return m, nil
	}
	var command tea.Cmd
	m.helpViewport, command = m.helpViewport.Update(key)
	return m, command
}

func (m *model) refreshHelp(gotoTop bool) {
	wasAtBottom := m.helpViewport.AtBottom()
	m.helpViewport.SetContent(renderHelpContent(m.dark))
	if gotoTop {
		m.helpViewport.GotoTop()
	} else if wasAtBottom {
		m.helpViewport.GotoBottom()
	}
}

func (m model) viewHelp() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · Help"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render("Commands stay out of the chat footer; open this guide whenever you need them."))
	body.WriteString("\n\n")
	body.WriteString(helpPanelStyle(max(36, m.width-4), m.dark).Render(m.helpViewport.View()))
	body.WriteString("\n\n")
	body.WriteString(helpStyle.Render("pgup/pgdn scroll · home/end jump · ctrl+h/esc back · ctrl+c quit"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func renderHelpContent(dark bool) string {
	var body strings.Builder
	writeHelpSection := func(title string, rows [][2]string) {
		if body.Len() > 0 {
			body.WriteString("\n\n")
		}
		body.WriteString(agentTraceTitleStyle(dark).Render(title))
		body.WriteString("\n")
		for _, row := range rows {
			body.WriteString(activeLabelStyle.Render(fmt.Sprintf("  %-18s", row[0])))
			body.WriteString(subtleStyle.Render(row[1]))
			body.WriteString("\n")
		}
	}

	writeHelpSection("SLASH COMMANDS", [][2]string{
		{"/plan [request]", "Grill, research, approve, and execute a work plan."},
		{"/commit", "Open the interactive commit workflow."},
		{"/clear", "Clear the current chat projection."},
		{"/model", "Choose default, subagent, and embedding models."},
		{"/provider", "Configure and select a model provider."},
		{"/loom", "Inspect Loom storage and garbage-collection settings."},
		{"/ignore", "Edit workspace discovery rules in .qignore."},
		{"/skills", "Interactively add, pull, remove, and reindex global/session skills."},
		{"/help", "Open this help screen."},
	})
	writeHelpSection("CHAT", [][2]string{
		{"enter", "Send the current message."},
		{"shift+enter", "Insert a newline."},
		{"ctrl+s", "Send the current message."},
		{"ctrl+l", "Clear the current chat projection."},
		{"ctrl+p", "Open provider settings."},
		{"ctrl+h", "Open or close this help screen."},
		{"ctrl+c", "Interrupt the active turn, or quit while idle."},
		{"esc", "Leave the current screen or quit chat."},
	})
	writeHelpSection("SUBAGENTS AND QUESTIONS", [][2]string{
		{"ctrl+g", "Expand or collapse the detailed subagent trace."},
		{"pgup/pgdn", "Scroll the active trace, question, or help panel."},
		{"↑/↓ or tab", "Move through question choices."},
		{"type", "Write a custom answer instead of choosing an option."},
	})
	writeHelpSection("COMMAND LINE", [][2]string{
		{"q commit", "Open the guided commit-message and commit TUI."},
	})
	return strings.TrimRight(body.String(), "\n")
}

func helpPanelStyle(width int, dark bool) lipgloss.Style {
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.Border{Left: "│"}).BorderLeft(true).
		BorderForeground(themedColor(dark, "99", "61")).
		PaddingLeft(1).Width(max(10, width-4))
}
