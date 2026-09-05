package app

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
)

type customManager struct {
	cursor, field   int
	entries         []subagent.ProfileEntry
	editing         bool
	original        *subagent.ProfileEntry
	inputs          []textinput.Model
	prompt          textarea.Model
	tools           map[string]bool
	picker          bool
	options         []string
	descriptions    map[string]string
	filter          textinput.Model
	pickCursor      int
	detail          string
	detailOffset    int
	panelOffset     int
	validationError string
	confirmDelete   bool
}

func (m model) enterCustom() (tea.Model, tea.Cmd) {
	m.input.Reset()
	m.screen = screenCustom
	m.custom = customManager{}
	m.reloadCustom()
	m.status = ""
	return m, nil
}
func (m *model) reloadCustom() {
	selectedProfilePath := ""
	if m.custom.cursor >= 0 && m.custom.cursor < len(m.custom.entries) {
		selectedProfilePath = m.custom.entries[m.custom.cursor].Path
	}
	m.custom.entries = m.customStore().List()
	m.custom.cursor = 0
	if selectedProfilePath != "" {
		for index, entry := range m.custom.entries {
			if entry.Path == selectedProfilePath {
				m.custom.cursor = index
				break
			}
		}
	}
	m.custom.panelOffset = 0
	m.custom.confirmDelete = false
}
func (m model) customCount() int {
	return len(m.custom.entries)
}
func (m model) beginCustomEdit(create bool) (tea.Model, tea.Cmd) {
	c := &m.custom
	c.confirmDelete = false
	c.original = nil
	c.field = 0
	c.tools = map[string]bool{}
	c.inputs = nil
	for i := 0; i < 7; i++ {
		t := textinput.New()
		t.SetStyles(textinput.DefaultStyles(m.dark))
		t.CharLimit = 0
		t.SetWidth(max(20, m.width-12))
		c.inputs = append(c.inputs, t)
	}
	c.prompt = textarea.New()
	c.prompt.SetStyles(textarea.DefaultStyles(m.dark))
	c.prompt.CharLimit = 0
	c.prompt.SetWidth(max(20, m.width-12))
	c.prompt.SetHeight(max(3, min(10, m.height-15)))
	c.inputs[0].Placeholder = "code-reader"
	c.inputs[1].Placeholder = "What this subagent is for"
	c.prompt.Placeholder = "Describe the subagent's responsibilities and expected output."
	c.inputs[2].SetValue("global")
	if m.workspaceStore != nil {
		c.inputs[2].SetValue("workspace")
	}
	c.inputs[3].SetValue("scout")
	if !create {
		if c.cursor >= len(c.entries) {
			return m, nil
		}
		e := c.entries[c.cursor]
		if e.Err != nil {
			m.status = e.Err.Error()
			return m, nil
		}
		c.original = &e
		p := e.Profile
		c.inputs[0].SetValue(p.Name)
		c.inputs[1].SetValue(p.Description)
		c.inputs[2].SetValue(e.Scope)
		c.inputs[3].SetValue(p.Role)
		c.prompt.SetValue(p.SystemPrompt)
		for _, t := range p.Tools {
			c.tools[t] = true
		}
	}
	c.editing = true
	c.validationError = ""
	m.status = ""
	return m, m.focusCustom()
}
func (m *model) focusCustom() tea.Cmd {
	for i := range m.custom.inputs {
		m.custom.inputs[i].Blur()
	}
	m.custom.prompt.Blur()
	if m.custom.field == len(m.custom.inputs)-1 {
		return nil
	}
	if m.custom.field == 5 || m.custom.field == 2 || m.custom.field == 3 {
		return nil
	}
	if m.custom.field == 4 {
		return m.custom.prompt.Focus()
	}
	return m.custom.inputs[m.custom.field].Focus()
}

func (m model) customFieldOptions() []string {
	if m.custom.field == 3 {
		return m.activeConfig().NativeRoles()
	}
	options := []string{"global"}
	if m.workspaceStore != nil {
		options = append(options, "workspace")
	}
	return options
}

func (m model) moveCustomField(delta int) (tea.Model, tea.Cmd) {
	m.custom.field = (m.custom.field + delta + len(m.custom.inputs)) % len(m.custom.inputs)
	command := m.focusCustom()
	return m, command
}
func (m model) openCustomPicker() (tea.Model, tea.Cmd) {
	c := &m.custom
	c.options = nil
	c.descriptions = map[string]string{}
	c.pickCursor = 0
	c.filter = textinput.New()
	c.filter.SetStyles(textinput.DefaultStyles(m.dark))
	c.filter.Placeholder = "Search…"
	c.filter.SetWidth(max(20, m.width-12))
	switch {
	case c.field == 2:
		c.options = m.customFieldOptions()
		c.descriptions["global"] = "Available in every workspace"
		c.descriptions["workspace"] = "Overrides a global profile with the same name here"
		c.filter.Placeholder = "Search scopes…"
	case c.field == 3:
		c.options = m.customFieldOptions()
		for _, role := range c.options {
			kind := "Custom model role"
			if config.IsAgentRole(role) {
				kind = "Built-in model role"
			}
			c.descriptions[role] = kind + " · " + m.customRoleModelSummary(role)
		}
		c.filter.Placeholder = "Search roles…"
	case c.field == 5:
		seen := map[string]bool{}
		if runtime := m.customTools(); runtime != nil {
			for _, t := range runtime.Tools() {
				if subagent.CustomToolAllowed(t.Function.Name) && !seen[t.Function.Name] {
					seen[t.Function.Name] = true
					c.options = append(c.options, t.Function.Name)
					source := "Built-in"
					if strings.HasPrefix(t.Function.Name, "mcp_") {
						source = "External MCP"
					}
					c.descriptions[t.Function.Name] = source + "\n" + t.Function.Description
				}
			}
		}
		for name := range c.tools {
			if !seen[name] {
				c.options = append(c.options, name)
				c.descriptions[name] = "Unavailable in current runtime"
			}
		}
		sort.Strings(c.options)
		c.filter.Placeholder = "Search tools…"
	default:
		return m, nil
	}
	c.picker = true
	if c.field == 2 || c.field == 3 {
		for i, value := range c.options {
			if value == c.inputs[c.field].Value() {
				c.pickCursor = i
				break
			}
		}
	}
	c.panelOffset = 0
	return m, c.filter.Focus()
}
func (c customManager) matches() []string {
	var out []string
	for _, s := range c.options {
		if strings.Contains(strings.ToLower(s+" "+c.descriptions[s]), strings.ToLower(c.filter.Value())) {
			out = append(out, s)
		}
	}
	return out
}
func (m model) updateCustom(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	c := &m.custom
	k := key.Keystroke()
	// Save belongs to the whole editor, including nested selection lists.
	// Handle it before a picker can consume the key as search input.
	if c.editing && (k == "ctrl+s" || k == "f2") {
		return m.saveCustom()
	}
	if c.detail == "" && (k == "pgdown" || k == "pgup") {
		text, width, height := m.customPanelContent()
		delta := max(1, height/2)
		if k == "pgup" {
			delta = -delta
		}
		maximum := max(0, len(strings.Split(ansi.Wrap(text, width, ""), "\n"))-height)
		c.panelOffset = max(0, min(maximum, c.panelOffset+delta))
		return m, nil
	}
	if c.detail != "" {
		if k == "esc" {
			c.detail = ""
			m.screen = screenChat
			return m, m.input.Focus()
		}
		width, height := m.customBodySize()
		lines := strings.Split(ansi.Wrap(c.detail, width, ""), "\n")
		switch k {
		case "down":
			c.detailOffset++
		case "up":
			c.detailOffset--
		case "pgdown":
			c.detailOffset += height
		case "pgup":
			c.detailOffset -= height
		case "home":
			c.detailOffset = 0
		case "end":
			c.detailOffset = len(lines)
		}
		c.detailOffset = max(0, min(c.detailOffset, max(0, len(lines)-height)))
		return m, nil
	}
	if c.confirmDelete {
		switch k {
		case "d", "y", "enter":
			c.confirmDelete = false
			return m.deleteCustom()
		case "n", "esc":
			c.confirmDelete = false
			m.status = "Delete canceled"
			return m, nil
		default:
			c.confirmDelete = false
			m.status = ""
		}
	}
	if c.picker {
		matches := c.matches()
		switch k {
		case "tab", "shift+tab":
			c.picker = false
			delta := 1
			if k == "shift+tab" {
				delta = -1
			}
			return m.moveCustomField(delta)
		case "space":
			if c.field == 5 && len(matches) > 0 {
				choice := matches[min(c.pickCursor, len(matches)-1)]
				c.tools[choice] = !c.tools[choice]
				return m, nil
			}
		case "esc":
			c.picker = false
			return m, m.focusCustom()
		case "up":
			c.panelOffset = 0
			c.pickCursor = max(0, c.pickCursor-1)
			return m, nil
		case "down":
			c.panelOffset = 0
			c.pickCursor = min(max(0, len(matches)-1), c.pickCursor+1)
			return m, nil
		case "enter":
			if len(matches) > 0 {
				choice := matches[min(c.pickCursor, len(matches)-1)]
				if c.field == 5 {
					c.tools[choice] = !c.tools[choice]
					return m, nil
				}
				c.inputs[c.field].SetValue(choice)
			}
			c.picker = false
			return m, m.focusCustom()
		}
		var cmd tea.Cmd
		c.filter, cmd = c.filter.Update(key)
		c.pickCursor = 0
		return m, cmd
	}
	if c.editing {
		if k == "ctrl+up" {
			return m.moveCustomField(-1)
		}
		if k == "ctrl+down" {
			return m.moveCustomField(1)
		}
		if (c.field == 2 || c.field == 3) && (k == "left" || k == "right" || k == "space") {
			options := m.customFieldOptions()
			index := 0
			for i, value := range options {
				if value == c.inputs[c.field].Value() {
					index = i
					break
				}
			}
			delta := 1
			if k == "left" {
				delta = -1
			}
			if len(options) > 0 {
				c.inputs[c.field].SetValue(options[(index+delta+len(options))%len(options)])
			}
			return m, nil
		}
		// Arrow keys match the other settings forms. Inside the multiline
		// prompt they remain text-navigation keys; Tab always changes fields.
		if (k == "up" || k == "down") && c.field != 4 {
			delta := 1
			if k == "up" {
				delta = -1
			}
			c.field = (c.field + delta + len(c.inputs)) % len(c.inputs)
			command := m.focusCustom()
			return m, command
		}
		switch k {
		case "esc":
			if c.field == 4 {
				return m.moveCustomField(-1)
			}
			c.editing = false
			c.validationError = ""
			m.status = "Edit canceled"
			return m, nil
		case "tab", "shift+tab":
			delta := 1
			if k == "shift+tab" {
				delta = -1
			}
			return m.moveCustomField(delta)
		case "enter":
			if c.field == len(c.inputs)-1 {
				return m.saveCustom()
			}
			if c.field == 0 || c.field == 1 {
				c.field++
				command := m.focusCustom()
				return m, command
			}
			if c.field == 2 || c.field == 3 || c.field == 5 {
				return m.openCustomPicker()
			}
		}
		if c.field == 0 && c.original != nil || c.field == 2 && c.original != nil {
			return m, nil
		}
		return m.updateCustomInput(key)
	}
	switch k {
	case "esc":
		m.screen = screenChat
		return m, m.input.Focus()
	case "up":
		c.panelOffset = 0
		c.cursor = max(0, c.cursor-1)
		m.status = ""
	case "down":
		c.panelOffset = 0
		c.cursor = min(max(0, m.customCount()-1), c.cursor+1)
		m.status = ""
	case "a":
		return m.beginCustomEdit(true)
	case "e", "enter":
		return m.beginCustomEdit(false)
	case "d":
		if target := c.customDeleteTarget(); target != "" {
			c.confirmDelete = true
			m.status = "Delete " + target + "?"
		}
	case "r":
		m.reloadCustom()
		m.status = "Reloaded"
	}
	return m, nil
}
func (m model) updateCustomInput(message tea.Msg) (tea.Model, tea.Cmd) {
	c := &m.custom
	var cmd tea.Cmd
	if c.picker {
		c.filter, cmd = c.filter.Update(message)
	} else if c.editing {
		if c.field == len(c.inputs)-1 {
			return m, nil
		}
		if c.field == 5 || c.field == 2 || c.field == 3 {
			return m, nil
		}
		if c.field == 4 {
			c.prompt, cmd = c.prompt.Update(message)
		} else if !(c.field == 0 && c.original != nil) && !(c.field == 2 && c.original != nil) {
			c.inputs[c.field], cmd = c.inputs[c.field].Update(message)
		}
	}
	return m, cmd
}
func (m model) saveCustom() (tea.Model, tea.Cmd) {
	c := &m.custom
	c.validationError = ""
	name := strings.TrimSpace(c.inputs[0].Value())
	savedScope := ""
	if !config.ValidCustomName(name) {
		message := "Name: use 1–64 lowercase letters, digits or hyphens; start with a letter."
		var suggestion strings.Builder
		for _, r := range strings.ToLower(name) {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				suggestion.WriteRune(r)
			} else if suggestion.Len() > 0 && !strings.HasSuffix(suggestion.String(), "-") {
				suggestion.WriteByte('-')
			}
		}
		candidate := strings.Trim(suggestion.String(), "-")
		if config.ValidCustomName(candidate) {
			message = "Name: use lowercase letters, digits or hyphens. Try " + candidate + "."
		}
		c.field = 0
		c.picker = false
		c.validationError = message
		m.status = "Not saved · " + message
		command := m.focusCustom()
		return m, command
	}
	var err error
	p := subagent.Profile{Version: 1, Name: name, Description: c.inputs[1].Value(), Role: strings.TrimSpace(c.inputs[3].Value()), SystemPrompt: c.prompt.Value(), Tools: []string{}}
	for toolName, selected := range c.tools {
		if selected {
			p.Tools = append(p.Tools, toolName)
		}
	}
	sort.Strings(p.Tools)
	if !m.activeConfig().HasNativeRole(p.Role) {
		err = fmt.Errorf("unknown native role %q", p.Role)
	} else {
		savedScope = c.inputs[2].Value()
		err = m.customStore().Save(p, savedScope, c.original)
	}
	if err != nil {
		m.status = err.Error()
		c.validationError = err.Error()
		return m, nil
	}
	c.editing = false
	c.picker = false
	c.filter.Blur()
	m.reloadCustom()
	for index, entry := range c.entries {
		if entry.Profile.Name == name && entry.Scope == savedScope {
			c.cursor = index
			break
		}
	}
	m.status = "Saved"
	return m, nil
}
func (m model) deleteCustom() (tea.Model, tea.Cmd) {
	c := &m.custom
	if c.cursor >= len(c.entries) {
		return m, nil
	}
	err := m.customStore().Delete(c.entries[c.cursor])
	if err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.reloadCustom()
	m.status = "Deleted; profile precedence refreshed"
	return m, nil
}
