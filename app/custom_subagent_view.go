package app

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/subagent"
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
	if index, found := c.selectedProfileIndex(); found {
		name := c.entries[index].Profile.Name
		if name == "" {
			name = filepath.Base(c.entries[index].Path)
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
	header.WriteString(titleStyle.Render("q · Subagents"))
	header.WriteString("\n")
	path := m.store.Path()
	if m.workspaceStore != nil {
		path = "workspace · " + filepath.Clean(m.workspaceStore.Root)
	}
	header.WriteString(subtleStyle.Render(customLine(path, width)))
	header.WriteString("\n")
	section := fmt.Sprintf("› Subagents %d", m.customCount())
	if m.custom.connections {
		section = "› ACP Connections"
	}
	header.WriteString(activeLabelStyle.Render(section))
	header.WriteString("\n\n")
	if m.custom.connections {
		body := m.viewCustomConnections(width, height)
		status := m.status
		if status == "" {
			status = "Connections are shared by external subagents"
		}
		help := "↑/↓ select · c test · a add · e edit · t enable/disable · d delete · esc subagents"
		if m.agentsMode != agentsModeList {
			help = "tab/↑/↓ field · enter save · esc cancel"
		}
		return frameStyle.Render(header.String() + body + "\n" + subtleStyle.Render(customLine(status, width)) + "\n" + helpStyle.Render(customLine(help, width)))
	}
	body := m.viewCustomLists(width, height)
	help := "↑/↓ select · a add · e edit · d delete · c ACP connections · r reload · esc back"
	if m.custom.detail != "" {
		body = customWindow(m.custom.detail, width, height, m.custom.detailOffset)
		help = "↑/↓ scroll · pgup/pgdn page · home/end jump · esc back"
	} else if m.custom.picker {
		body = m.viewCustomPicker(width, height)
		if m.custom.field == customFieldTools || m.custom.field == customFieldDelegates {
			help = "↑/↓ select · space/enter toggle · tab done · ctrl+s save"
		} else {
			help = "↑/↓ select · enter choose · type filter · esc back"
		}
	} else if m.custom.editing {
		body = m.viewCustomForm(width, height)
		help = "tab/↑/↓ field · enter next/choose · ctrl+s/F2 save · esc cancel"
		if m.custom.field == customFieldPrompt {
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
			if m.custom.field == customFieldPrompt {
				help = "tab next · shift+tab/esc back"
			}
		default:
			help = "a add · e edit · d del · c ACP · esc"
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

func (m model) viewCustomConnections(width, height int) string {
	if m.agentsMode == agentsModeEditConnection {
		return customWindow(m.viewACPConnectionForm(), width, height, 0)
	}
	ids := agentConnectionIDs(m.agentsDraft.Agents)
	if len(ids) == 0 {
		return customWindow(subtleStyle.Render("No ACP connections · press a to register one"), width, height, 0)
	}
	var lines []string
	start, end := lspVisibleRange(len(ids), m.agentsCursor[1], max(1, height/2))
	for index := start; index < end; index++ {
		id := ids[index]
		connection := m.agentsDraft.Agents.Connections[id]
		state := "enabled"
		if connection.Disabled {
			state = "disabled"
		}
		if probe := m.agentsProbe[id]; probe != "" {
			state += " · " + probe
		}
		prefix := "  "
		if index == m.agentsCursor[1] {
			prefix = "› "
		}
		lines = append(lines, activeLabelStyle.Render(customLine(prefix+id, width)))
		lines = append(lines, subtleStyle.Render(customLine("    "+agentConnectionEndpoint(connection)+" · "+state, width)))
	}
	return customWindow(strings.Join(lines, "\n"), width, height, 0)
}

func (m model) customSelectionDetail() string {
	c := m.custom
	if definition, found := c.selectedFixed(); found {
		info := definition.Info
		access := "READ-ONLY"
		if info.MutatesWorkspace {
			access = "MUTATES WORKSPACE"
		}
		var b strings.Builder
		b.WriteString(activeLabelStyle.Render(info.Name))
		b.WriteString("\n")
		b.WriteString(subtleStyle.Render(strings.ToUpper(info.Kind) + " · " + access))
		b.WriteString("\n")
		if info.Description != "" {
			b.WriteString("\n")
			b.WriteString(info.Description)
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(agentTraceTitleStyle(m.dark).Render("CONFIGURATION"))
		b.WriteString("\n")
		b.WriteString("Kind    ")
		b.WriteString(info.Kind)
		b.WriteString("\n")
		b.WriteString("Role    ")
		b.WriteString(info.Role)
		b.WriteString("\n")
		if info.Kind == subagent.AgentKindExternal {
			connection, _, available := externalDefinitionConnection(m.activeConfig(), definition)
			if !available {
				connection = "unavailable"
			}
			b.WriteString("ACP     ")
			b.WriteString(connection)
			b.WriteString("\n")
		} else {
			b.WriteString("Model   ")
			b.WriteString(m.customRoleModelSummary(info.Role))
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("Tools      %d available\n", len(definition.Tools)))
		b.WriteString(fmt.Sprintf("Delegates  %d granted\n", len(definition.Delegates)))
		b.WriteString("\n")
		b.WriteString(agentTraceTitleStyle(m.dark).Render("SYSTEM PROMPT"))
		b.WriteString("\n")
		b.WriteString(definition.SystemPrompt)
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(themedColor(m.dark, "99", "61")).Render("TOOLS"))
		b.WriteString("\n")
		if len(definition.Tools) == 0 {
			b.WriteString("No tools")
		} else {
			b.WriteString(strings.Join(definition.Tools, "\n"))
		}
		b.WriteString("\n\n")
		b.WriteString(agentTraceTitleStyle(m.dark).Render("DELEGATES"))
		b.WriteString("\n")
		if len(definition.Delegates) == 0 {
			b.WriteString("No delegates")
		} else {
			b.WriteString(strings.Join(definition.Delegates, "\n"))
		}
		return b.String()
	}
	index, found := c.selectedProfileIndex()
	if !found {
		return "Add a profile with a, then choose its role, prompt, and tools."
	}
	e := c.entries[index]
	p := e.Profile
	state := e.Scope
	if e.Shadowed {
		state += " · shadowed by workspace profile"
	} else {
		state += " · active"
	}
	var b strings.Builder
	b.WriteString(activeLabelStyle.Render(p.Name))
	b.WriteString("\n")
	if e.Err != nil {
		b.WriteString(errorStyle.Render(strings.ToUpper(state) + " · INVALID"))
		b.WriteString("\n")
	} else {
		b.WriteString(subtleStyle.Render(strings.ToUpper(state) + " PROFILE"))
		b.WriteString("\n")
	}
	if e.Err != nil {
		b.WriteString("\n")
		b.WriteString(errorStyle.Render("Definition error: " + e.Err.Error()))
		b.WriteString("\n")
	}
	if p.Description != "" {
		b.WriteString("\n")
		b.WriteString(p.Description)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(agentTraceTitleStyle(m.dark).Render("CONFIGURATION"))
	b.WriteString("\n")
	b.WriteString("Kind    ")
	b.WriteString(p.EffectiveKind())
	b.WriteString("\n")
	if p.EffectiveKind() == subagent.AgentKindExternal {
		b.WriteString("ACP     ")
		b.WriteString(p.Agent)
		if _, _, available := externalDefinitionConnection(m.activeConfig(), subagent.AgentDefinition{Info: subagent.DelegateInfo{Kind: subagent.AgentKindExternal}, Connection: p.Agent}); !available {
			b.WriteString(" · unavailable")
		}
		b.WriteString("\n")
	} else {
		b.WriteString("Role    ")
		b.WriteString(p.Role)
		b.WriteString("\n")
		b.WriteString("Model   ")
		b.WriteString(m.customRoleModelSummary(p.Role))
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Tools      %d selected\n", len(p.Tools))
	fmt.Fprintf(&b, "Delegates  %d selected\n", len(p.Delegates))
	b.WriteString(subtleStyle.Render("Source  " + e.Path))
	b.WriteString("\n")
	b.WriteString("\n")
	b.WriteString(agentTraceTitleStyle(m.dark).Render("SYSTEM PROMPT"))
	b.WriteString("\n")
	b.WriteString(p.SystemPrompt)
	b.WriteString("\n\n")
	b.WriteString(agentTraceTitleStyle(m.dark).Render("TOOLS"))
	b.WriteString("\n")
	if len(p.Tools) == 0 {
		b.WriteString("No tools")
	} else {
		b.WriteString(strings.Join(p.Tools, "\n"))
	}
	b.WriteString("\n\n")
	b.WriteString(agentTraceTitleStyle(m.dark).Render("DELEGATES"))
	b.WriteString("\n")
	if len(p.Delegates) == 0 {
		b.WriteString("No delegates")
	} else {
		b.WriteString(strings.Join(p.Delegates, "\n"))
	}
	return b.String()
}

func (m model) customList(width, height int) string {
	var lines []string
	count := m.customCount()
	visibleItems := max(1, height/2)
	start, end := lspVisibleRange(count, m.custom.cursor, visibleItems)
	for i := start; i < end; i++ {
		if i < len(m.custom.fixed) {
			definition := m.custom.fixed[i]
			access := "read-only"
			if definition.Info.MutatesWorkspace {
				access = "mutates workspace"
			}
			prefix := "  "
			if i == m.custom.cursor {
				prefix = "› "
			}
			line := customLine(prefix+definition.Info.Name, width)
			if i == m.custom.cursor {
				line = activeLabelStyle.Render(line)
			}
			state := definition.Info.Kind
			if definition.Info.Kind == subagent.AgentKindExternal {
				if _, _, available := externalDefinitionConnection(m.activeConfig(), definition); !available {
					state += " · unavailable"
				}
			}
			meta := subtleStyle.Render(customLine("    "+definition.Info.Role+" · "+state+" · "+access, width))
			lines = append(lines, line, meta)
			continue
		}
		e := m.custom.entries[i-len(m.custom.fixed)]
		label := e.Profile.Name
		if label == "" {
			label = filepath.Base(e.Path)
		}
		toolLabel := fmt.Sprintf("%d tools", len(e.Profile.Tools))
		if len(e.Profile.Tools) == 1 {
			toolLabel = "1 tool"
		}
		delegateLabel := fmt.Sprintf("%d delegates", len(e.Profile.Delegates))
		identity := e.Profile.Role
		kind := e.Profile.EffectiveKind()
		if kind == subagent.AgentKindExternal {
			identity = "ACP " + e.Profile.Agent
			toolLabel = "host tools"
			delegateLabel = "no delegates"
		}
		meta := strings.Trim(strings.Join([]string{identity, kind, e.Scope, toolLabel, delegateLabel}, " · "), " ·")
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
			label = "Unassigned"
		}
		cursor := "  "
		if i == c.pickCursor {
			cursor = "› "
		}
		mark := "○ "
		if c.field == customFieldTools || c.field == customFieldDelegates {
			selected := c.tools[name]
			if c.field == customFieldDelegates {
				selected = c.delegates[name]
			}
			if selected {
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
		label := name
		if label == "" {
			label = "Unassigned"
		}
		detail = label + "\n" + c.descriptions[name]
	}
	return c.filter.View() + "\n" + customWindow(strings.Join(lines, "\n"), width, listHeight, 0) + "\n" + agentTraceTitleStyle(m.dark).Render("DETAILS") + "\n" + customWindow(detail, width, max(1, height-listHeight-3), c.panelOffset)
}

func (m model) viewCustomForm(width, height int) string {
	c := m.custom
	formWidth := width
	labels := map[int]string{
		customFieldName: "Name *", customFieldDescription: "Description", customFieldScope: "Scope",
		customFieldKind: "Kind *", customFieldRole: "Role *", customFieldACP: "ACP connection *",
		customFieldPrompt: "System prompt *", customFieldAccess: "Workspace access",
		customFieldTools: "Tools", customFieldDelegates: "Delegates", customFieldSave: "Save profile",
	}
	formTitle := "NEW PROFILE"
	formDescription := "Inner subagents use q models and tools; external subagents forward the prompt to a registered ACP connection."
	if c.fixedExternal != nil {
		formTitle = "EDIT " + strings.ToUpper(c.fixedExternal.Info.Name)
		formDescription = "Builtin instructions are fixed. Only its ACP connection can be changed."
		labels[customFieldSave] = "Save connection"
	}
	if c.original != nil {
		formTitle = strings.Replace(formTitle, "NEW ", "EDIT ", 1)
	}
	lines := []string{
		agentTraceTitleStyle(m.dark).Render(formTitle),
		subtleStyle.Render(customLine(formDescription, formWidth)),
		"",
	}
	focusLine := len(lines)
	fields := m.customVisibleFields()
	for _, i := range fields {
		label := labels[i]
		if i == customFieldSave {
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
		if i == customFieldPrompt {
			value = strings.ReplaceAll(c.prompt.Value(), "\n", " ")
		}
		if i == customFieldTools {
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
		if i == customFieldDelegates {
			var names []string
			for n, on := range c.delegates {
				if on {
					names = append(names, n)
				}
			}
			sort.Strings(names)
			value = strings.Join(names, ", ")
			if value == "" {
				value = "No delegates"
			}
		}
		if i == customFieldACP && value == "" {
			value = "Unassigned"
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
		if i == customFieldPrompt {
			prompt := c.prompt
			prompt.SetWidth(max(1, formWidth-2))
			prompt.SetHeight(max(2, height-len(fields)-3))
			lines = append(lines, strings.Split(prompt.View(), "\n")...)
			lines = append(lines, subtleStyle.Render(customLine("Tab: next · Shift+Tab/Esc: previous", formWidth)))
		} else if i == customFieldTools {
			lines = append(lines, customLine(value, formWidth), subtleStyle.Render(customLine("Enter: select built-in / MCP tools", formWidth)))
		} else if i == customFieldDelegates {
			lines = append(lines, customLine(value, formWidth), subtleStyle.Render(customLine("Enter: select callable subagents", formWidth)))
		} else if slices.Contains([]int{customFieldScope, customFieldKind, customFieldRole, customFieldACP, customFieldAccess}, i) {
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
