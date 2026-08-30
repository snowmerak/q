package app

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type slashCommand struct {
	name        string
	arguments   string
	description string
}

func (c slashCommand) usage() string {
	if c.arguments != "" {
		return c.name + " " + c.arguments
	}
	return c.name
}

// Keep the local help screen and completion menu on the same command catalog.
var localSlashCommands = []slashCommand{
	{"/plan", "[request]", "Grill, research, approve, and execute a work plan."},
	{"/auto-approve", "[on|off|status]", "Persistently control automatic plan approval."},
	{"/auto-resolve", "[on|off|status]", "Persistently control automatic plan clarification."},
	{"/autonomous", "[on|off|status]", "Persistently control both plan automation settings."},
	{"/agent:search", "<query>", "Run the configured ACP Search agent."},
	{"/commit", "", "Open the interactive commit workflow."},
	{"/changes", "", "Browse file diffs with syntax highlighting (read only)."},
	{"/sessions", "", "Resume a saved workspace session."},
	{"/new", "", "Create a new session."},
	{"/clear", "", "Empty the current session in place."},
	{"/learn", "[on|off|status]", "Checkpoint or control Thinker learning for this workspace."},
	{"/model", "", "Choose workspace, default, subagent, and embedding models."},
	{"/gateway", "", "Configure Gateway network, API keys, and providers."},
	{"/library", "", "Configure the global Library listener defaults."},
	{"/loom", "", "Inspect Loom storage and garbage-collection settings."},
	{"/ignore", "", "Edit workspace discovery rules in .qignore."},
	{"/skills", "", "Interactively add, pull, remove, and reindex global/session skills."},
	{"/lsp", "", "Configure global language servers and workspace project roots."},
	{"/mcp", "", "Configure external MCP tool servers and per-role assignments."},
	{"/agents", "", "Configure ACP agents and external role assignments."},
	{"/help", "", "Open this help screen."},
}

type slashCompletionState struct {
	query     string
	selected  int
	dismissed bool
}

func (m *model) syncSlashCompletion() {
	if value := m.input.Value(); value != m.slashCompletion.query {
		m.slashCompletion = slashCompletionState{query: value}
	}
}

func (m model) slashCommands() []slashCommand {
	if remote, ok := m.client.(*acpRemoteClient); ok {
		return remote.slashCommands()
	}
	return localSlashCommands
}

func (m model) slashCompletionMatches() []slashCommand {
	if m.screen != screenChat || m.client == nil || m.waiting || m.asking || m.initializing || m.submitPending || !m.input.Focused() {
		return nil
	}
	value := m.input.Value()
	if !strings.HasPrefix(value, "/") || strings.ContainsFunc(value, unicode.IsSpace) || m.input.Column() != len([]rune(value)) {
		return nil
	}
	if m.slashCompletion.query == value && m.slashCompletion.dismissed {
		return nil
	}
	var matches []slashCommand
	for _, command := range m.slashCommands() {
		if strings.HasPrefix(command.name, value) {
			matches = append(matches, command)
		}
	}
	return matches
}

func (m model) slashCompletionIndex(count int) int {
	if m.slashCompletion.query != m.input.Value() {
		return 0
	}
	return min(max(0, m.slashCompletion.selected), max(0, count-1))
}

func (m *model) handleSlashCompletionKey(key tea.KeyPressMsg) bool {
	m.syncSlashCompletion()
	matches := m.slashCompletionMatches()
	if len(matches) == 0 {
		return false
	}
	selected := m.slashCompletionIndex(len(matches))
	switch key.String() {
	case "up", "shift+tab":
		m.slashCompletion.selected = (selected - 1 + len(matches)) % len(matches)
	case "down":
		m.slashCompletion.selected = (selected + 1) % len(matches)
	case "esc":
		m.slashCompletion.dismissed = true
	case "tab", "enter":
		command := matches[selected]
		if key.String() == "enter" && m.input.Value() == command.name {
			// Preserve Enter-to-run for fully typed commands, including the IME
			// grace period. Selecting an incomplete command only fills the input.
			m.slashCompletion.dismissed = true
			return false
		}
		value := command.name
		if command.arguments != "" {
			value += " "
		}
		m.input.SetValue(value)
		m.input.CursorEnd()
		m.slashCompletion = slashCompletionState{query: value, dismissed: true}
	default:
		return false
	}
	return true
}

func (m model) renderSlashCompletion(width, height int) string {
	matches := m.slashCompletionMatches()
	if len(matches) == 0 || width < 12 || height < 3 {
		return ""
	}
	width = min(width, 76)
	innerWidth := width - 4 // border and one cell of padding on either side
	selected := m.slashCompletionIndex(len(matches))
	headerRows := 0
	if height >= 4 {
		headerRows = 1
	}
	visible := min(6, len(matches), height-2-headerRows)
	start := min(max(0, selected-visible+1), len(matches)-visible)
	background := themedColor(m.dark, "235", "255")
	normal := lipgloss.NewStyle().Foreground(themedColor(m.dark, "252", "238")).Background(background).Width(innerWidth)
	active := normal.Bold(true).Foreground(themedColor(m.dark, "231", "17")).Background(themedColor(m.dark, "60", "153"))
	var rows []string
	if headerRows > 0 {
		header := fmt.Sprintf("Commands · %d/%d", selected+1, len(matches))
		rows = append(rows, normal.Faint(true).Render(ansi.Truncate(header, innerWidth, "…")))
	}
	for index := start; index < start+visible; index++ {
		command := matches[index]
		prefix, style := "  ", normal
		if index == selected {
			prefix, style = "› ", active
		}
		row := prefix + command.usage()
		if description := singleLineCommandText(command.description); description != "" {
			row += "  " + description
		}
		rows = append(rows, style.Render(ansi.Truncate(row, innerWidth, "…")))
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(themedColor(m.dark, "99", "61")).Background(background).
		Padding(0, 1).Render(strings.Join(rows, "\n"))
}

func singleLineCommandText(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.Strip(value))
	return strings.Join(strings.Fields(value), " ")
}

func (m model) overlaySlashCompletion(content string) string {
	if len(m.slashCompletionMatches()) == 0 {
		return content
	}
	inputOffset := m.chatInputOffset()
	availableHeight := inputOffset - m.renderedChatHeaderHeight() - 1
	popup := m.renderSlashCompletion(m.chatFrameWidth()-frameStyle.GetHorizontalFrameSize(), availableHeight)
	if popup == "" {
		return content
	}
	// Overlay the transcript instead of resizing it: input/IME cursor position
	// and the user's transcript scroll position stay fixed as matches change.
	composed := lipgloss.NewCompositor(
		lipgloss.NewLayer(content),
		lipgloss.NewLayer(popup).X(frameStyle.GetPaddingLeft()).
			Y(frameStyle.GetPaddingTop()+inputOffset-lipgloss.Height(popup)).Z(1),
	).Render()
	// The compositor trims trailing blank cells; retain the base frame's size.
	return lipgloss.NewStyle().Width(lipgloss.Width(content)).Height(lipgloss.Height(content)).Render(composed)
}
