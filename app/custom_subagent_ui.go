package app

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
)

const (
	customFieldName = iota
	customFieldDescription
	customFieldScope
	customFieldKind
	customFieldRole
	customFieldACP
	customFieldPrompt
	customFieldAccess
	customFieldTools
	customFieldDelegates
	customFieldSave
	customFieldCount
)

type customManager struct {
	cursor, field   int
	fixed           []subagent.AgentDefinition
	entries         []subagent.ProfileEntry
	editing         bool
	fixedExternal   *subagent.AgentDefinition
	connections     bool
	original        *subagent.ProfileEntry
	inputs          []textinput.Model
	prompt          textarea.Model
	tools           map[string]bool
	delegates       map[string]bool
	picker          bool
	options         []string
	descriptions    map[string]string
	filter          textinput.Model
	pickCursor      int
	detail          string
	detailOffset    int
	panelOffset     int
	panelFocused    bool
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
	selectedID := ""
	if definition, ok := m.custom.selectedFixed(); ok {
		selectedID = definition.Info.Name
	} else if index, ok := m.custom.selectedProfileIndex(); ok {
		selectedID = m.custom.entries[index].Path
	}
	m.custom.fixed = subagent.PublicAgentDefinitions()
	if registry, err := subagent.NewRegistry(m.custom.fixed); err == nil {
		for index := range m.custom.fixed {
			if definition, found := registry.Get(m.custom.fixed[index].Info.Name); found {
				m.custom.fixed[index] = definition
			}
		}
	}
	m.custom.entries = m.customStore().List()
	m.custom.cursor = 0
	if selectedID != "" {
		matched := false
		for index, definition := range m.custom.fixed {
			if definition.Info.Name == selectedID {
				m.custom.cursor = index
				matched = true
				break
			}
		}
		if !matched {
			for index, entry := range m.custom.entries {
				if entry.Path == selectedID {
					m.custom.cursor = len(m.custom.fixed) + index
					break
				}
			}
		}
	}
	m.custom.panelOffset = 0
	m.custom.panelFocused = false
	m.custom.confirmDelete = false
}
func (m model) customCount() int {
	return len(m.custom.fixed) + len(m.custom.entries)
}

func (c customManager) selectedFixed() (subagent.AgentDefinition, bool) {
	if c.cursor >= 0 && c.cursor < len(c.fixed) {
		return c.fixed[c.cursor], true
	}
	return subagent.AgentDefinition{}, false
}

func (c customManager) selectedProfileIndex() (int, bool) {
	index := c.cursor - len(c.fixed)
	return index, index >= 0 && index < len(c.entries)
}

func (m model) beginCustomEdit(create bool) (tea.Model, tea.Cmd) {
	c := &m.custom
	c.confirmDelete = false
	c.panelFocused = false
	c.original = nil
	c.fixedExternal = nil
	c.field = 0
	c.tools = map[string]bool{}
	c.delegates = map[string]bool{}
	c.inputs = nil
	for range customFieldCount {
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
	c.inputs[customFieldName].Placeholder = "code-reader"
	c.inputs[customFieldDescription].Placeholder = "What this subagent is for"
	c.prompt.Placeholder = "Describe the subagent's responsibilities and expected output."
	c.inputs[customFieldScope].SetValue("global")
	if m.workspaceStore != nil {
		c.inputs[customFieldScope].SetValue("workspace")
	}
	c.inputs[customFieldKind].SetValue(subagent.AgentKindInner)
	c.inputs[customFieldRole].SetValue("scout")
	c.inputs[customFieldAccess].SetValue("read-only")
	if !create {
		if definition, fixed := c.selectedFixed(); fixed {
			if definition.Info.Kind != subagent.AgentKindExternal {
				m.status = definition.Info.Name + " is a fixed inner definition"
				return m, nil
			}
			copy := definition
			c.fixedExternal = &copy
			c.inputs[customFieldACP].SetValue(m.activeConfig().Agents.Roles[definition.Info.Role].Agent)
			c.prompt.SetValue(definition.SystemPrompt)
			c.field = customFieldACP
			c.editing = true
			c.validationError = ""
			m.status = ""
			return m, m.focusCustom()
		}
		index, found := c.selectedProfileIndex()
		if !found {
			return m, nil
		}
		e := c.entries[index]
		if e.Err != nil {
			m.status = e.Err.Error()
			return m, nil
		}
		c.original = &e
		p := e.Profile
		c.inputs[customFieldName].SetValue(p.Name)
		c.inputs[customFieldDescription].SetValue(p.Description)
		c.inputs[customFieldScope].SetValue(e.Scope)
		c.inputs[customFieldKind].SetValue(p.EffectiveKind())
		c.inputs[customFieldRole].SetValue(p.Role)
		c.inputs[customFieldACP].SetValue(p.Agent)
		if p.MutatesWorkspace {
			c.inputs[customFieldAccess].SetValue("mutates workspace")
		}
		c.prompt.SetValue(p.SystemPrompt)
		for _, t := range p.Tools {
			c.tools[t] = true
		}
		for _, delegate := range p.Delegates {
			c.delegates[delegate] = true
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
	if m.custom.field == customFieldSave {
		return nil
	}
	if slices.Contains([]int{customFieldTools, customFieldDelegates, customFieldScope, customFieldKind, customFieldRole, customFieldACP, customFieldAccess}, m.custom.field) {
		return nil
	}
	if m.custom.field == customFieldPrompt {
		return m.custom.prompt.Focus()
	}
	return m.custom.inputs[m.custom.field].Focus()
}

func (m model) customFieldOptions() []string {
	switch m.custom.field {
	case customFieldRole:
		return m.activeConfig().NativeRoles()
	case customFieldKind:
		return []string{subagent.AgentKindInner, subagent.AgentKindExternal}
	case customFieldACP:
		return append([]string{""}, agentConnectionIDs(m.activeConfig().Agents)...)
	case customFieldAccess:
		return []string{"read-only", "mutates workspace"}
	}
	options := []string{"global"}
	if m.workspaceStore != nil {
		options = append(options, "workspace")
	}
	return options
}

func (m model) customKind() string {
	if m.custom.fixedExternal != nil {
		return subagent.AgentKindExternal
	}
	kind := strings.TrimSpace(m.custom.inputs[customFieldKind].Value())
	if kind == "" {
		return subagent.AgentKindInner
	}
	return kind
}

func (m model) customVisibleFields() []int {
	if m.custom.fixedExternal != nil {
		return []int{customFieldACP, customFieldSave}
	}
	if m.customKind() == subagent.AgentKindExternal {
		return []int{customFieldName, customFieldDescription, customFieldScope, customFieldKind, customFieldACP, customFieldPrompt, customFieldAccess, customFieldSave}
	}
	return []int{customFieldName, customFieldDescription, customFieldScope, customFieldKind, customFieldRole, customFieldPrompt, customFieldTools, customFieldDelegates, customFieldSave}
}

func (m model) moveCustomField(delta int) (tea.Model, tea.Cmd) {
	fields := m.customVisibleFields()
	index := slices.Index(fields, m.custom.field)
	if index < 0 {
		index = 0
	}
	m.custom.field = fields[(index+delta+len(fields))%len(fields)]
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
	case c.field == customFieldScope:
		c.options = m.customFieldOptions()
		c.descriptions["global"] = "Available in every workspace"
		c.descriptions["workspace"] = "Overrides a global profile with the same name here"
		c.filter.Placeholder = "Search scopes…"
	case c.field == customFieldKind:
		c.options = m.customFieldOptions()
		c.descriptions[subagent.AgentKindInner] = "Run with a q-native model role and explicitly selected q tools"
		c.descriptions[subagent.AgentKindExternal] = "Forward the request and this profile's system prompt to an ACP connection"
		c.filter.Placeholder = "Search kinds…"
	case c.field == customFieldRole:
		c.options = m.customFieldOptions()
		for _, role := range c.options {
			kind := "Custom model role"
			if config.IsAgentRole(role) {
				kind = "Built-in model role"
			}
			c.descriptions[role] = kind + " · " + m.customRoleModelSummary(role)
		}
		c.filter.Placeholder = "Search roles…"
	case c.field == customFieldACP:
		c.options = m.customFieldOptions()
		c.descriptions[""] = "Leave this builtin external subagent unavailable"
		for _, id := range c.options {
			if id == "" {
				continue
			}
			connection := m.activeConfig().Agents.Connections[id]
			state := "enabled"
			if connection.Disabled {
				state = "disabled"
			}
			c.descriptions[id] = agentConnectionEndpoint(connection) + " · " + state
		}
		c.filter.Placeholder = "Search ACP connections…"
	case c.field == customFieldAccess:
		c.options = m.customFieldOptions()
		c.descriptions["read-only"] = "Reject ACP permission requests that can mutate the workspace"
		c.descriptions["mutates workspace"] = "Automatically accept allowed ACP permission options"
		c.filter.Placeholder = "Search access modes…"
	case c.field == customFieldTools:
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
	case c.field == customFieldDelegates:
		registry, err := buildSubagentRegistry(m.customStore())
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
		self := subagent.CanonicalProfileID(c.inputs[customFieldScope].Value(), strings.TrimSpace(c.inputs[customFieldName].Value()))
		for _, info := range registry.List() {
			if info.Name == self || c.inputs[customFieldScope].Value() == "global" && strings.HasPrefix(info.Name, "workspace/") {
				continue
			}
			c.options = append(c.options, info.Name)
			access := "read-only"
			if info.MutatesWorkspace {
				access = "mutates workspace"
			}
			state := info.Source + " · " + info.Kind + " · " + access
			if info.Kind == subagent.AgentKindExternal {
				definition, _ := registry.Get(info.Name)
				if _, _, available := externalDefinitionConnection(m.activeConfig(), definition); !available {
					state += " · unavailable"
				}
			}
			c.descriptions[info.Name] = state + "\n" + info.Description
		}
		for name := range c.delegates {
			if _, found := c.descriptions[name]; !found {
				c.options = append(c.options, name)
				c.descriptions[name] = "Unavailable in current registry"
			}
		}
		sort.Strings(c.options)
		c.filter.Placeholder = "Search delegates…"
	default:
		return m, nil
	}
	c.picker = true
	if slices.Contains([]int{customFieldScope, customFieldKind, customFieldRole, customFieldACP, customFieldAccess}, c.field) {
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

func (m *model) scrollCustomPanel(key string) bool {
	text, width, height := m.customPanelContent()
	lines := strings.Split(ansi.Wrap(text, width, ""), "\n")
	maximum := max(0, len(lines)-height)
	switch key {
	case "up", "k":
		m.custom.panelOffset--
	case "down", "j":
		m.custom.panelOffset++
	case "pgup":
		m.custom.panelOffset -= max(1, height-1)
	case "pgdown":
		m.custom.panelOffset += max(1, height-1)
	case "home":
		m.custom.panelOffset = 0
	case "end":
		m.custom.panelOffset = maximum
	default:
		return false
	}
	m.custom.panelOffset = max(0, min(maximum, m.custom.panelOffset))
	return true
}

func (m model) updateCustom(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	c := &m.custom
	if c.connections {
		return m.updateCustomConnections(key)
	}
	k := key.Keystroke()
	// Save belongs to the whole editor, including nested selection lists.
	// Handle it before a picker can consume the key as search input.
	if c.editing && (k == "ctrl+s" || k == "f2") {
		return m.saveCustom()
	}
	if c.detail == "" && c.panelFocused && !c.editing && !c.picker {
		switch k {
		case "esc", "left", "tab", "shift+tab":
			c.panelFocused = false
			m.status = ""
			return m, nil
		}
		if m.scrollCustomPanel(k) {
			return m, nil
		}
		return m, nil
	}
	if c.detail == "" && (k == "pgdown" || k == "pgup") {
		m.scrollCustomPanel(k)
		return m, nil
	}
	if c.detail != "" {
		if k == "esc" {
			c.detail = ""
			if m.isStandaloneScreen(screenCustom) {
				return m, tea.Quit
			}
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
			if (c.field == customFieldTools || c.field == customFieldDelegates) && len(matches) > 0 {
				choice := matches[min(c.pickCursor, len(matches)-1)]
				if c.field == customFieldTools {
					c.tools[choice] = !c.tools[choice]
				} else {
					c.delegates[choice] = !c.delegates[choice]
				}
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
				if c.field == customFieldTools || c.field == customFieldDelegates {
					if c.field == customFieldTools {
						c.tools[choice] = !c.tools[choice]
					} else {
						c.delegates[choice] = !c.delegates[choice]
					}
					return m, nil
				}
				c.inputs[c.field].SetValue(choice)
				if c.field == customFieldKind {
					c.tools = map[string]bool{}
					c.delegates = map[string]bool{}
					if choice == subagent.AgentKindInner && c.inputs[customFieldRole].Value() == "" {
						c.inputs[customFieldRole].SetValue(config.AgentRoleScout)
					}
				}
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
		if slices.Contains([]int{customFieldScope, customFieldKind, customFieldRole, customFieldACP, customFieldAccess}, c.field) && (k == "left" || k == "right" || k == "space") {
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
				if c.field == customFieldKind {
					c.tools = map[string]bool{}
					c.delegates = map[string]bool{}
				}
			}
			return m, nil
		}
		// Arrow keys match the other settings forms. Inside the multiline
		// prompt they remain text-navigation keys; Tab always changes fields.
		if (k == "up" || k == "down") && c.field != customFieldPrompt {
			delta := 1
			if k == "up" {
				delta = -1
			}
			return m.moveCustomField(delta)
		}
		switch k {
		case "esc":
			if c.field == customFieldPrompt {
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
			if c.field == customFieldSave {
				return m.saveCustom()
			}
			if c.field == customFieldName || c.field == customFieldDescription {
				return m.moveCustomField(1)
			}
			if slices.Contains([]int{customFieldScope, customFieldKind, customFieldRole, customFieldACP, customFieldAccess, customFieldTools, customFieldDelegates}, c.field) {
				return m.openCustomPicker()
			}
		}
		if c.field == customFieldName && c.original != nil || c.field == customFieldScope && c.original != nil {
			return m, nil
		}
		return m.updateCustomInput(key)
	}
	switch k {
	case "esc":
		if m.isStandaloneScreen(screenCustom) {
			return m, tea.Quit
		}
		m.screen = screenChat
		return m, m.input.Focus()
	case "up":
		c.panelOffset = 0
		c.cursor = max(0, c.cursor-1)
		m.status = ""
	case "down", "j":
		c.panelOffset = 0
		c.cursor = min(max(0, m.customCount()-1), c.cursor+1)
		m.status = ""
	case "k":
		c.panelOffset = 0
		c.cursor = max(0, c.cursor-1)
		m.status = ""
	case "right", "tab":
		c.panelFocused = true
		m.status = ""
	case "a":
		return m.beginCustomEdit(true)
	case "c":
		return m.enterCustomConnections()
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
		if c.field == customFieldSave {
			return m, nil
		}
		if slices.Contains([]int{customFieldTools, customFieldDelegates, customFieldScope, customFieldKind, customFieldRole, customFieldACP, customFieldAccess}, c.field) {
			return m, nil
		}
		if c.field == customFieldPrompt {
			c.prompt, cmd = c.prompt.Update(message)
		} else if !(c.field == customFieldName && c.original != nil) && !(c.field == customFieldScope && c.original != nil) {
			c.inputs[c.field], cmd = c.inputs[c.field].Update(message)
		}
	}
	return m, cmd
}
func (m model) saveCustom() (tea.Model, tea.Cmd) {
	c := &m.custom
	c.validationError = ""
	if c.fixedExternal != nil {
		definition := *c.fixedExternal
		value := cloneConfigForAgents(m.activeConfig())
		if value.Agents.Roles == nil {
			value.Agents.Roles = make(map[string]config.AgentConfig)
		}
		assignment := value.Agents.Roles[definition.Info.Role]
		assignment.Agent = strings.TrimSpace(c.inputs[customFieldACP].Value())
		value.Agents.Roles[definition.Info.Role] = assignment
		if err := value.Validate(); err != nil {
			c.validationError = err.Error()
			m.status = err.Error()
			return m, nil
		}
		c.editing = false
		c.picker = false
		c.fixedExternal = nil
		m.agentsDraft = value
		return m.saveAgentsSettings()
	}
	name := strings.TrimSpace(c.inputs[customFieldName].Value())
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
		c.field = customFieldName
		c.picker = false
		c.validationError = message
		m.status = "Not saved · " + message
		command := m.focusCustom()
		return m, command
	}
	var err error
	p := subagent.Profile{
		Version: 1, Name: name, Description: c.inputs[customFieldDescription].Value(),
		Kind: m.customKind(), SystemPrompt: c.prompt.Value(),
	}
	if p.Kind == subagent.AgentKindExternal {
		p.Agent = strings.TrimSpace(c.inputs[customFieldACP].Value())
		p.MutatesWorkspace = c.inputs[customFieldAccess].Value() == "mutates workspace"
	} else {
		p.Role = strings.TrimSpace(c.inputs[customFieldRole].Value())
		p.Tools = []string{}
		p.Delegates = []string{}
		for toolName, selected := range c.tools {
			if selected {
				p.Tools = append(p.Tools, toolName)
			}
		}
		sort.Strings(p.Tools)
		for delegate, selected := range c.delegates {
			if selected {
				p.Delegates = append(p.Delegates, delegate)
			}
		}
		sort.Strings(p.Delegates)
	}
	if p.Kind == subagent.AgentKindInner && !m.activeConfig().HasNativeRole(p.Role) {
		err = fmt.Errorf("unknown native role %q", p.Role)
	} else if p.Kind == subagent.AgentKindExternal {
		if _, found := m.activeConfig().Agents.Connections[p.Agent]; !found {
			err = fmt.Errorf("unknown ACP agent connection %q; register it with c", p.Agent)
		}
	}
	if err == nil {
		savedScope = c.inputs[customFieldScope].Value()
		if err = m.validateCustomDelegates(p, savedScope, c.original); err == nil {
			err = m.customStore().Save(p, savedScope, c.original)
		}
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
			c.cursor = len(c.fixed) + index
			break
		}
	}
	m.status = "Saved"
	return m, nil
}

func (m model) validateCustomDelegates(profile subagent.Profile, scope string, original *subagent.ProfileEntry) error {
	definitions := subagent.PublicAgentDefinitions()
	for _, entry := range m.customStore().List() {
		if entry.Err != nil || original != nil && entry.Path == original.Path {
			continue
		}
		definition, err := subagent.DefinitionForProfile(entry)
		if err != nil {
			return err
		}
		definitions = append(definitions, definition)
	}
	definition, err := subagent.DefinitionForProfile(subagent.ProfileEntry{Profile: profile, Scope: scope})
	if err != nil {
		return err
	}
	definitions = append(definitions, definition)
	_, err = subagent.NewRegistry(definitions)
	return err
}
func (m model) deleteCustom() (tea.Model, tea.Cmd) {
	c := &m.custom
	index, found := c.selectedProfileIndex()
	if !found {
		return m, nil
	}
	target := c.entries[index]
	targetID := subagent.CanonicalProfileID(target.Scope, target.Profile.Name)
	for _, entry := range m.customStore().List() {
		if entry.Err != nil || entry.Path == target.Path {
			continue
		}
		if slices.Contains(entry.Profile.Delegates, targetID) {
			m.status = fmt.Sprintf("Cannot delete %s; referenced by %s/%s", targetID, entry.Scope, entry.Profile.Name)
			return m, nil
		}
	}
	err := m.customStore().Delete(target)
	if err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.reloadCustom()
	m.status = "Deleted; profile precedence refreshed"
	return m, nil
}

func (m model) enterCustomConnections() (tea.Model, tea.Cmd) {
	m.custom.connections = true
	m.custom.panelFocused = false
	m.custom.editing = false
	m.custom.picker = false
	m.custom.confirmDelete = false
	m.agentsDraft = cloneConfigForAgents(m.activeConfig())
	m.agentsCursor = [2]int{}
	m.agentsMode = agentsModeList
	m.agentsEditID = ""
	m.agentsBusy = false
	m.agentsProbe = make(map[string]string)
	m.status = ""
	return m, nil
}

func (m model) updateCustomConnections(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.agentsBusy {
		return m, nil
	}
	if m.agentsMode != agentsModeList {
		return m.updateAgentsForm(key)
	}
	ids := agentConnectionIDs(m.agentsDraft.Agents)
	switch key.Keystroke() {
	case "up", "k":
		if len(ids) > 0 {
			m.agentsCursor[1] = (m.agentsCursor[1] - 1 + len(ids)) % len(ids)
		}
	case "down", "j":
		if len(ids) > 0 {
			m.agentsCursor[1] = (m.agentsCursor[1] + 1) % len(ids)
		}
	case "a":
		return m.beginAgentConnectionAdd()
	case "e", "enter":
		return m.beginAgentConnectionEdit()
	case "d", "delete", "backspace":
		return m.deleteAgentConnection()
	case "t":
		return m.toggleAgentConnection()
	case "c":
		return m.probeAgentConnection()
	case "esc":
		m.custom.connections = false
		m.status = ""
		m.reloadCustom()
	}
	return m, nil
}
