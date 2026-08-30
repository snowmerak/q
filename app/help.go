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
	for index := range m.lspInputs {
		m.lspInputs[index].Blur()
	}
	for index := range m.mcpInputs {
		m.mcpInputs[index].Blur()
	}
	for index := range m.agentsInputs {
		m.agentsInputs[index].Blur()
	}
	m.modelFilter.Blur()
	m.embeddingDimensions.Blur()
	m.modelContextWindow.Blur()
	m.blurGatewayInputs()
	m.blurLibraryInputs()
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
	case screenLSP:
		if m.lspMode != lspModeList {
			return m, m.lspInputs[m.lspFormFocus].Focus()
		}
		return m, nil
	case screenMCP:
		if m.mcpMode != mcpModeList {
			return m, m.mcpInputs[m.mcpFormFocus].Focus()
		}
		return m, nil
	case screenAgents:
		if m.agentsMode != agentsModeList {
			return m, m.agentsInputs[m.agentsFormFocus].Focus()
		}
		return m, nil
	case screenGatewayNetwork:
		command := m.gatewayNetworkFocusCommand()
		return m, command
	case screenGatewayKeys:
		if m.gatewayKeyAdding {
			return m, m.gatewayKeyAlias.Focus()
		}
		return m, nil
	case screenLibrary:
		command := m.libraryNetworkFocusCommand()
		return m, command
	case screenSessions:
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
		if m.isStandaloneScreen(screenHelp) {
			return m, tea.Quit
		}
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
	if m.clientIsACPRemote() {
		m.helpViewport.SetContent(renderACPClientHelpContent(m.dark))
	} else {
		m.helpViewport.SetContent(renderHelpContent(m.dark))
	}
	if gotoTop {
		m.helpViewport.GotoTop()
	} else if wasAtBottom {
		m.helpViewport.GotoBottom()
	}
}

func renderACPClientHelpContent(dark bool) string {
	var body strings.Builder
	body.WriteString(agentTraceTitleStyle(dark).Render("ACP CLIENT"))
	body.WriteString("\n")
	body.WriteString(activeLabelStyle.Render(fmt.Sprintf("  %-18s", "slash commands")))
	body.WriteString(subtleStyle.Render("Forwarded unchanged to the connected agent."))
	body.WriteString("\n")
	body.WriteString(activeLabelStyle.Render(fmt.Sprintf("  %-18s", "/clear")))
	body.WriteString(subtleStyle.Render("Ask the connected agent to empty the current session."))
	body.WriteString("\n")
	body.WriteString(activeLabelStyle.Render(fmt.Sprintf("  %-18s", "/new")))
	body.WriteString(subtleStyle.Render("Close the current session and start a new ACP session."))
	body.WriteString("\n\n")
	body.WriteString(agentTraceTitleStyle(dark).Render("CHAT"))
	body.WriteString("\n")
	for _, row := range [][2]string{
		{"/", "Show local session commands and commands advertised by the connected agent."},
		{"↑/↓, tab / enter", "Select and complete a command; Escape dismisses the popup."},
		{"enter", "Send the current message."},
		{"shift+enter", "Insert a newline."},
		{"ctrl+l", "Close the current session and start a new ACP session."},
		{"ctrl+h", "Open or close this local help screen."},
		{"ctrl+c", "Interrupt the active ACP turn, or quit while idle."},
		{"esc", "Quit the ACP client."},
	} {
		body.WriteString(activeLabelStyle.Render(fmt.Sprintf("  %-18s", row[0])))
		body.WriteString(subtleStyle.Render(row[1]))
		body.WriteString("\n")
	}
	return body.String()
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
	help := "pgup/pgdn scroll · home/end jump · ctrl+h/esc back · ctrl+c quit"
	if m.isStandaloneScreen(screenHelp) {
		help = "pgup/pgdn scroll · home/end jump · ctrl+h/esc quit · ctrl+c quit"
	}
	body.WriteString(helpStyle.Render(help))
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
		labelWidth := 18
		for _, row := range rows {
			labelWidth = max(labelWidth, len([]rune(row[0])))
		}
		for _, row := range rows {
			body.WriteString(activeLabelStyle.Render(fmt.Sprintf("  %-*s", labelWidth, row[0])))
			body.WriteString(subtleStyle.Render(row[1]))
			body.WriteString("\n")
		}
	}

	var commands [][2]string
	for _, command := range localSlashCommands {
		commands = append(commands, [2]string{command.usage(), command.description})
	}
	writeHelpSection("SLASH COMMANDS", commands)
	writeHelpSection("COMMAND COMPLETION", [][2]string{
		{"/", "Show commands above the input; keep typing to filter."},
		{"↑/↓", "Select a command without scrolling the transcript."},
		{"tab / enter", "Complete the selection; Enter runs a fully typed command."},
		{"esc", "Dismiss the command popup without quitting."},
	})
	writeHelpSection("CHAT", [][2]string{
		{"enter", "Send the current message."},
		{"shift+enter", "Insert a newline."},
		{"ctrl+o", "Collapse or expand all tool result bodies in chat and subagent traces."},
		{"ctrl+s", "Send the current message."},
		{"ctrl+l", "Clear the current chat projection."},
		{"ctrl+p", "Open Gateway settings."},
		{"ctrl+h", "Open or close this help screen."},
		{"ctrl+c", "Interrupt the active turn, or quit while idle."},
		{"esc", "Leave the current screen or quit chat."},
	})
	writeHelpSection("SESSIONS", [][2]string{
		{"↑/↓ or j/k", "Move through saved workspace sessions."},
		{"enter", "Resume the selected session."},
		{"n", "Create and enter a new session."},
		{"r", "Refresh titles and recent activity."},
		{"esc", "Return to chat, or quit from the startup picker."},
	})
	writeHelpSection("CHANGES", [][2]string{
		{"↑/↓ or j/k", "Select a changed file; staged and unstaged diffs are separate."},
		{"tab / enter", "Switch file/diff panes, or focus the selected diff."},
		{"pgup/pgdn", "Page through files or the focused diff."},
		{"←/→", "Scroll the focused diff horizontally without wrapping code."},
		{"r", "Reload repository changes and invalidate cached previews."},
		{"esc", "Return to chat without staging or modifying files."},
	})
	writeHelpSection("SUBAGENTS AND QUESTIONS", [][2]string{
		{"ctrl+g", "Expand or collapse the detailed subagent trace."},
		{"pgup/pgdn", "Scroll the active trace, question, or help panel."},
		{"↑/↓ or tab", "Move through question choices."},
		{"type", "Write a custom answer instead of choosing an option."},
	})
	writeHelpSection("COMMAND LINE", [][2]string{
		{"q sprint <request...>", "Run an autonomous plan from clarification through review."},
		{"q commit", "Open the guided commit-message and commit TUI."},
		{"q memory", "Run Workspace Memory as a dedicated foreground service."},
		{"q library", "Run the global Library as a dedicated foreground service."},
		{"q library config", "Configure the Library's default host and fixed port."},
		{"q gateway", "Run only the standalone OpenAI-compatible Gateway."},
		{"q gateway config", "Configure Gateway network, API keys, and providers."},
		{"q model", "Configure main, embedding, and subagent models."},
		{"q mcp", "Configure external MCP tool servers and per-role assignments."},
		{"q agents", "Configure ACP agents and assign the Search role."},
		{"q skills", "Manage global and current-workspace Agent Skills."},
		{"q lsp", "Configure language servers and discover workspace roots."},
		{"q ignore", "Edit the current workspace's .qignore rules."},
		{"q help", "Open this help screen without starting q services."},
	})
	return strings.TrimRight(body.String(), "\n")
}

func helpPanelStyle(width int, dark bool) lipgloss.Style {
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.Border{Left: "│"}).BorderLeft(true).
		BorderForeground(themedColor(dark, "99", "61")).
		PaddingLeft(1).Width(max(10, width-4))
}
