package app

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func customLine(s string, width int) string {
	return ansi.Truncate(strings.ReplaceAll(s, "\n", " "), max(1, width), "…")
}

// customWindow wraps detail text and keeps its scroll position within the viewport.
func customWindow(text string, width, height, offset int) string {
	width = max(1, width)
	height = max(1, height)
	lines := strings.Split(ansi.Wrap(text, width, ""), "\n")
	offset = max(0, min(offset, max(0, len(lines)-height)))
	return lipgloss.NewStyle().Width(width).Height(height).MaxHeight(height).Render(strings.Join(lines[offset:min(len(lines), offset+height)], "\n"))
}

func (m model) customBodySize() (int, int) {
	return max(1, m.width-frameStyle.GetHorizontalFrameSize()), max(1, m.height-frameStyle.GetVerticalFrameSize()-7)
}

func (m model) customRoleModelSummary(name string) string {
	value := m.activeConfig()
	agent, err := value.EffectiveAgent(name)
	if err != nil {
		return "unavailable"
	}
	configured := value.Agents.Roles[name]
	if agent.Group != "" {
		return "group/" + agent.Group
	}
	if configured.Model == "" && configured.Group == "" {
		return "inherit → " + agent.Model
	}
	return agent.Model
}

func (c customManager) customDeleteTarget() string {
	if c.cursor >= 0 && c.cursor < len(c.entries) {
		name := c.entries[c.cursor].Profile.Name
		if name == "" {
			name = filepath.Base(c.entries[c.cursor].Path)
		}
		return "profile " + name
	}
	return ""
}

// Match the detail viewport used by each layout so paging cannot overscroll.
func (m model) customPanelContent() (string, int, int) {
	width, height := m.customBodySize()
	c := m.custom
	if c.picker {
		matches := c.matches()
		text := ""
		if len(matches) > 0 {
			name := matches[min(c.pickCursor, len(matches)-1)]
			text = name + "\n" + c.descriptions[name]
		}
		return text, width, max(1, height-max(1, height/2-2)-3)
	}
	if c.editing {
		return "", width, height
	}
	if width >= 76 {
		return m.customSelectionDetail(), width - max(24, width/3) - 2, max(1, height-1)
	}
	return m.customSelectionDetail(), width, max(1, height-min(4, max(1, (height-2)/3))-3)
}

func (m model) viewCustom() string {
	width, height := m.customBodySize()
	var header strings.Builder
	header.WriteString(titleStyle.Render("q · Subagents") + "\n")
	path := m.store.Path()
	if m.workspaceStore != nil {
		path = "workspace · " + filepath.Clean(m.workspaceStore.Root)
	}
	header.WriteString(subtleStyle.Render(customLine(path, width)) + "\n")
	header.WriteString(activeLabelStyle.Render(fmt.Sprintf("› Subagents %d", len(m.custom.entries))) + "\n\n")
	body := m.viewCustomLists(width, height)
	help := "↑/↓ select · a add · e edit · d delete · r reload · esc back"
	if m.custom.detail != "" {
		body = customWindow(m.custom.detail, width, height, m.custom.detailOffset)
		help = "↑/↓ scroll · pgup/pgdn page · home/end jump · esc back"
	} else if m.custom.picker {
		body = m.viewCustomPicker(width, height)
		if m.custom.field == 5 {
			help = "↑/↓ select · space/enter toggle · tab done · ctrl+s save"
		} else {
			help = "↑/↓ select · enter choose · type filter · esc back"
		}
	} else if m.custom.editing {
		body = m.viewCustomForm(width, height)
		help = "tab/↑/↓ field · enter next/choose · ctrl+s/F2 save · esc cancel"
		if m.custom.field == 4 {
			help = "tab next · shift+tab/esc previous · ctrl+s save"
		}
	}
	status := m.status
	if status == "" {
		status = "pgup/pgdn scroll details"
	}
	if width < 70 {
		switch {
		case m.custom.detail != "":
			help = "↑/↓ · pgup/pgdn · esc back"
		case m.custom.picker:
			help = "enter select · ctrl+s save · esc"
		case m.custom.editing:
			help = "tab/↑/↓ · enter · F2 save · esc"
			if m.custom.field == 4 {
				help = "tab next · shift+tab/esc back"
			}
		default:
			help = "a add · e edit · d del · esc"
		}
	}
	if m.custom.confirmDelete {
		help = "d/y/enter confirm · n/esc cancel"
	}
	statusStyle := subtleStyle
	if m.custom.validationError != "" {
		statusStyle = errorStyle
	} else if m.custom.confirmDelete {
		statusStyle = titleStyle
	}
	return frameStyle.Render(header.String() + body + "\n" + statusStyle.Render(customLine(status, width)) + "\n" + helpStyle.Render(customLine(help, width)))
}

func (m model) customSelectionDetail() string {
	c := m.custom
	if c.cursor >= len(c.entries) {
		return "Add a profile with a, then choose its role, prompt, and tools."
	}
	e := c.entries[c.cursor]
	p := e.Profile
	state := e.Scope
	if e.Shadowed {
		state += " · shadowed by workspace profile"
	} else {
		state += " · active"
	}
	var b strings.Builder
	b.WriteString(activeLabelStyle.Render(p.Name) + "\n")
	if e.Err != nil {
		b.WriteString(errorStyle.Render(strings.ToUpper(state)+" · INVALID") + "\n")
	} else {
		b.WriteString(subtleStyle.Render(strings.ToUpper(state)+" PROFILE") + "\n")
	}
	if e.Err != nil {
		b.WriteString("\n" + errorStyle.Render("Definition error: "+e.Err.Error()) + "\n")
	}
	if p.Description != "" {
		b.WriteString("\n" + p.Description + "\n")
	}
	b.WriteString("\n" + agentTraceTitleStyle(m.dark).Render("CONFIGURATION") + "\n")
	b.WriteString("Role    " + p.Role + "\n")
	b.WriteString("Model   " + m.customRoleModelSummary(p.Role) + "\n")
	b.WriteString(fmt.Sprintf("Tools   %d selected\n", len(p.Tools)))
	b.WriteString(subtleStyle.Render("Source  "+e.Path) + "\n")
	b.WriteString("\n" + agentTraceTitleStyle(m.dark).Render("SYSTEM PROMPT") + "\n" + p.SystemPrompt + "\n\n" + agentTraceTitleStyle(m.dark).Render("TOOLS") + "\n")
	if len(p.Tools) == 0 {
		b.WriteString("No tools")
	} else {
		b.WriteString(strings.Join(p.Tools, "\n"))
	}
	return b.String()
}

func (m model) customList(width, height int) string {
	var lines []string
	count := m.customCount()
	visibleItems := max(1, height/2)
	start, end := lspVisibleRange(count, m.custom.cursor, visibleItems)
	for i := start; i < end; i++ {
		e := m.custom.entries[i]
		label := e.Profile.Name
		if label == "" {
			label = filepath.Base(e.Path)
		}
		toolLabel := fmt.Sprintf("%d tools", len(e.Profile.Tools))
		if len(e.Profile.Tools) == 1 {
			toolLabel = "1 tool"
		}
		meta := strings.Trim(strings.Join([]string{e.Profile.Role, e.Scope, toolLabel}, " · "), " ·")
		if e.Shadowed {
			meta += " · shadowed"
		}
		invalid := e.Err != nil
		if invalid {
			meta += " · invalid"
		}
		prefix := "  "
		if i == m.custom.cursor {
			prefix = "› "
		}
		line := customLine(prefix+label, width)
		metaLine := subtleStyle.Render(customLine("    "+meta, width))
		if invalid {
			metaLine = errorStyle.Render(customLine("    "+meta, width))
		}
		if i == m.custom.cursor {
			line = activeLabelStyle.Render(line)
		}
		lines = append(lines, line, metaLine)
	}
	if count == 0 {
		lines = append(lines, subtleStyle.Render("No entries · press a"))
	}
	return customWindow(strings.Join(lines, "\n"), width, height, 0)
}

func (m model) viewCustomLists(width, height int) string {
	heading := agentTraceTitleStyle(m.dark)
	if width >= 76 {
		left := max(24, width/3)
		right := width - left - 2
		return lipgloss.JoinHorizontal(lipgloss.Top, heading.Render("LIST")+"\n"+m.customList(left, height-1), "  ", heading.Render("DETAILS")+"\n"+customWindow(m.customSelectionDetail(), right, height-1, m.custom.panelOffset))
	}
	listHeight := min(4, max(1, (height-2)/3))
	return heading.Render("LIST") + "\n" + m.customList(width, listHeight) + "\n" + heading.Render("DETAILS") + "\n" + customWindow(m.customSelectionDetail(), width, max(1, height-listHeight-3), m.custom.panelOffset)
}

func (m model) viewCustomPicker(width, height int) string {
	c := m.custom
	matches := c.matches()
	listHeight := max(1, height/2-2)
	var lines []string
	start, end := lspVisibleRange(len(matches), c.pickCursor, listHeight)
	for i := start; i < end; i++ {
		name := matches[i]
		label := name
		if label == "" {
			label = "Inherit / default"
		}
		cursor := "  "
		if i == c.pickCursor {
			cursor = "› "
		}
		mark := "○ "
		if c.field == 5 {
			if c.tools[name] {
				mark = "[x] "
			} else {
				mark = "[ ] "
			}
		} else if c.inputs[c.field].Value() == name {
			mark = "● "
		}
		line := customLine(cursor+mark+label, width)
		if i == c.pickCursor {
			line = activeLabelStyle.Render(line)
		}
		lines = append(lines, line)
	}
	if len(matches) == 0 {
		lines = append(lines, subtleStyle.Render("No matching options"))
	}
	detail := ""
	if len(matches) > 0 {
		name := matches[min(c.pickCursor, len(matches)-1)]
		detail = name + "\n" + c.descriptions[name]
	}
	return c.filter.View() + "\n" + customWindow(strings.Join(lines, "\n"), width, listHeight, 0) + "\n" + agentTraceTitleStyle(m.dark).Render("DETAILS") + "\n" + customWindow(detail, width, max(1, height-listHeight-3), c.panelOffset)
}

func (m model) viewCustomForm(width, height int) string {
	c := m.custom
	formWidth := width
	labels := []string{"Name *", "Description", "Scope", "Role *", "System prompt *", "Tools", "Save profile"}
	formTitle := "NEW PROFILE"
	formDescription := "Define a runnable subagent with instructions, a model role, and an explicit tool set."
	if c.original != nil {
		formTitle = strings.Replace(formTitle, "NEW ", "EDIT ", 1)
	}
	lines := []string{
		agentTraceTitleStyle(m.dark).Render(formTitle),
		subtleStyle.Render(customLine(formDescription, formWidth)),
		"",
	}
	focusLine := len(lines)
	for i, label := range labels {
		if i == len(labels)-1 {
			line := "  [ " + label + " ] · Enter"
			if c.field == i {
				focusLine = len(lines)
				line = activeLabelStyle.Render("› [ " + label + " ] · Enter")
			}
			lines = append(lines, line)
			if c.field == i && c.validationError != "" {
				lines = append(lines, customLine(c.validationError, formWidth))
			}
			continue
		}
		value := c.inputs[i].Value()
		if i == 4 {
			value = strings.ReplaceAll(c.prompt.Value(), "\n", " ")
		}
		if i == 5 {
			var names []string
			for n, on := range c.tools {
				if on {
					names = append(names, n)
				}
			}
			sort.Strings(names)
			value = strings.Join(names, ", ")
			if value == "" {
				value = "No tools"
			}
		}
		if i != c.field {
			lines = append(lines, subtleStyle.Render(customLine("  "+label+": "+value, formWidth)))
			continue
		}
		focusLine = len(lines)
		lines = append(lines, activeLabelStyle.Render(customLine("› "+label, formWidth)))
		if c.validationError != "" {
			lines = append(lines, activeLabelStyle.Render(customLine(c.validationError, formWidth)))
		}
		if i == 4 {
			prompt := c.prompt
			prompt.SetWidth(max(1, formWidth-2))
			prompt.SetHeight(max(2, height-len(labels)-3))
			lines = append(lines, strings.Split(prompt.View(), "\n")...)
			lines = append(lines, subtleStyle.Render(customLine("Tab: next · Shift+Tab/Esc: previous", formWidth)))
		} else if i == 5 {
			lines = append(lines, customLine(value, formWidth), subtleStyle.Render(customLine("Enter: select built-in / MCP tools", formWidth)))
		} else if i == 2 || i == 3 {
			lines = append(lines, activeLabelStyle.Render(customLine("‹ "+value+" ›", formWidth)))
			hint := "←/→: cycle · Enter: list"
			lines = append(lines, subtleStyle.Render(customLine(hint, formWidth)))
		} else {
			input := c.inputs[i]
			input.SetWidth(max(1, formWidth-3))
			lines = append(lines, input.View())
			hint := "Enter: next · Tab/↑/↓: field"
			if i == 0 {
				hint = "a-z, 0-9, hyphen · Enter: next"
			}
			lines = append(lines, subtleStyle.Render(customLine(hint, formWidth)))
		}
	}
	text := strings.Join(lines, "\n")
	return customWindow(text, width, height, max(0, focusLine-height+3))
}
