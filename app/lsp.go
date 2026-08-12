package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/snowmerak/q/config"
	qlsp "github.com/snowmerak/q/lsp"
	"github.com/snowmerak/q/workspace"
)

type lspScreenMode uint8

type lspDiscoveryMsg struct {
	id        uint64
	scanRoots bool
	roots     []qlsp.RootConfig
	servers   []qlsp.DiscoveredServer
	err       error
}

const (
	lspModeList lspScreenMode = iota
	lspModeEditServer
	lspModeEditRoot
)

func (m model) enterLSP() (tea.Model, tea.Cmd) {
	if m.workspaceStore == nil {
		m.status = "Workspace storage is unavailable"
		return m, m.input.Focus()
	}
	workspaceConfig, err := m.workspaceStore.LoadLSP()
	if err != nil {
		m.status = err.Error()
		return m, m.input.Focus()
	}
	m.screen = screenLSP
	m.input.Blur()
	m.lspMode = lspModeList
	m.lspPanel = 0
	m.lspCursor = [2]int{}
	m.lspDraftGlobal = cloneLSPGlobal(m.config.LSP)
	m.lspOriginalGlobal = cloneLSPGlobal(m.config.LSP)
	m.lspDraftWorkspace = cloneLSPWorkspace(workspaceConfig)
	m.lspOriginalWorkspace = cloneLSPWorkspace(workspaceConfig)
	m.lspDiscardArmed = false
	m.lspBusy = false
	m.lspDiscoveryID++
	m.status = ""
	m.resize(m.width, m.height)
	return m, nil
}

func (m model) updateLSP(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.lspMode != lspModeList {
		return m.updateLSPForm(key)
	}
	switch key.String() {
	case "tab", "left", "right":
		m.lspPanel = 1 - m.lspPanel
		m.status = ""
		return m, nil
	case "up", "k":
		m.moveLSPCursor(-1)
		return m, nil
	case "down", "j":
		m.moveLSPCursor(1)
		return m, nil
	case "a":
		return m.beginLSPAdd()
	case "e", "enter":
		return m.beginLSPEdit()
	case "d", "delete", "backspace":
		return m.deleteLSPEntry()
	case " ":
		return m.toggleLSPEntry()
	case "m":
		if m.lspPanel == 0 {
			return m.makeLSPServerDefault()
		}
	case "r":
		return m.startLSPDiscovery()
	case "ctrl+s":
		return m.saveLSPSettings()
	case "esc":
		if m.lspModified() && !m.lspDiscardArmed {
			m.lspDiscardArmed = true
			m.status = "Unsaved changes · press esc again to discard or ctrl+s to save"
			return m, nil
		}
		m.screen = screenChat
		m.status = ""
		if m.isStandaloneScreen(screenLSP) {
			return m, tea.Quit
		}
		return m, m.input.Focus()
	}
	return m, nil
}

func (m model) updateLSPForm(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	fieldCount := 4
	if m.lspMode == lspModeEditRoot {
		fieldCount = 3
	}
	switch key.String() {
	case "esc":
		m.cancelLSPForm()
		return m, nil
	case "tab", "down":
		m.lspFormFocus = (m.lspFormFocus + 1) % fieldCount
		return m, m.focusLSPForm()
	case "shift+tab", "up":
		m.lspFormFocus = (m.lspFormFocus - 1 + fieldCount) % fieldCount
		return m, m.focusLSPForm()
	case "ctrl+s", "enter":
		return m.acceptLSPForm()
	}
	var command tea.Cmd
	m.lspInputs[m.lspFormFocus], command = m.lspInputs[m.lspFormFocus].Update(key)
	return m, command
}

func (m *model) moveLSPCursor(delta int) {
	length := len(m.lspServerIDs())
	if m.lspPanel == 1 {
		length = len(m.lspDraftWorkspace.Roots)
	}
	if length == 0 {
		m.lspCursor[m.lspPanel] = 0
		return
	}
	m.lspCursor[m.lspPanel] = (m.lspCursor[m.lspPanel] + delta + length) % length
}

func (m model) beginLSPAdd() (tea.Model, tea.Cmd) {
	for index := range m.lspInputs {
		m.lspInputs[index].Reset()
		m.lspInputs[index].Blur()
	}
	m.lspFormFocus = 0
	m.lspEditingIndex = -1
	m.lspEditID = ""
	if m.lspPanel == 0 {
		m.lspMode = lspModeEditServer
		m.lspInputs[3].SetValue("[]")
	} else {
		m.lspMode = lspModeEditRoot
	}
	m.status = ""
	return m, m.lspInputs[0].Focus()
}

func (m model) beginLSPEdit() (tea.Model, tea.Cmd) {
	if m.lspPanel == 0 {
		ids := m.lspServerIDs()
		if len(ids) == 0 {
			return m, nil
		}
		index := min(m.lspCursor[0], len(ids)-1)
		id := ids[index]
		server := m.lspDraftGlobal.Servers[id]
		arguments, _ := json.Marshal(server.Args)
		values := []string{id, strings.Join(server.Languages, ", "), server.Command, string(arguments)}
		for index := range m.lspInputs {
			m.lspInputs[index].SetValue(values[index])
			m.lspInputs[index].Blur()
		}
		m.lspMode, m.lspEditID, m.lspEditingIndex = lspModeEditServer, id, index
	} else {
		if len(m.lspDraftWorkspace.Roots) == 0 {
			return m, nil
		}
		index := min(m.lspCursor[1], len(m.lspDraftWorkspace.Roots)-1)
		root := m.lspDraftWorkspace.Roots[index]
		values := []string{root.Path, root.Language, root.Server}
		for field := range 3 {
			m.lspInputs[field].SetValue(values[field])
			m.lspInputs[field].Blur()
		}
		m.lspMode, m.lspEditingIndex = lspModeEditRoot, index
	}
	m.lspFormFocus = 0
	m.status = ""
	return m, m.lspInputs[0].Focus()
}

func (m model) acceptLSPForm() (tea.Model, tea.Cmd) {
	if m.lspMode == lspModeEditServer {
		id := strings.TrimSpace(m.lspInputs[0].Value())
		languages, err := qlsp.NormalizeLanguages(strings.Split(m.lspInputs[1].Value(), ","))
		if err != nil {
			m.status = err.Error()
			return m, m.lspInputs[m.lspFormFocus].Focus()
		}
		var arguments []string
		decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(m.lspInputs[3].Value())))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&arguments); err != nil {
			m.status = "Arguments must be a JSON string array · " + err.Error()
			return m, m.lspInputs[3].Focus()
		}
		server := qlsp.ServerConfig{Languages: languages, Command: strings.TrimSpace(m.lspInputs[2].Value()), Args: arguments}
		if existing, ok := m.lspDraftGlobal.Servers[m.lspEditID]; ok {
			server.Disabled = existing.Disabled
		}
		candidate := cloneLSPGlobal(m.lspDraftGlobal)
		if candidate.Servers == nil {
			candidate.Servers = make(map[string]qlsp.ServerConfig)
		}
		if id != m.lspEditID {
			if _, exists := candidate.Servers[id]; exists {
				m.status = "LSP server profile already exists · " + id
				return m, m.lspInputs[0].Focus()
			}
			delete(candidate.Servers, m.lspEditID)
			for language, serverID := range candidate.Languages {
				if serverID == m.lspEditID {
					candidate.Languages[language] = id
				}
			}
			for index := range m.lspDraftWorkspace.Roots {
				if m.lspDraftWorkspace.Roots[index].Server == m.lspEditID {
					m.lspDraftWorkspace.Roots[index].Server = id
				}
			}
		}
		candidate.Servers[id] = server
		if candidate.Languages == nil {
			candidate.Languages = make(map[string]string)
		}
		for _, language := range languages {
			if candidate.Languages[language] == "" {
				candidate.Languages[language] = id
			}
		}
		for language, serverID := range candidate.Languages {
			if serverID == id && !containsStringFold(languages, language) {
				delete(candidate.Languages, language)
			}
		}
		if err := candidate.Validate(); err != nil {
			m.status = err.Error()
			return m, m.lspInputs[m.lspFormFocus].Focus()
		}
		m.lspDraftGlobal = candidate
	} else {
		root, err := qlsp.NormalizeRoot(qlsp.RootConfig{
			Path: m.lspInputs[0].Value(), Language: m.lspInputs[1].Value(), Server: m.lspInputs[2].Value(), Source: qlsp.RootSourceManual,
		})
		if err != nil {
			m.status = err.Error()
			return m, m.lspInputs[m.lspFormFocus].Focus()
		}
		candidate := cloneLSPWorkspace(m.lspDraftWorkspace)
		if m.lspEditingIndex >= 0 {
			root.Source = candidate.Roots[m.lspEditingIndex].Source
			root.Disabled = candidate.Roots[m.lspEditingIndex].Disabled
			candidate.Roots[m.lspEditingIndex] = root
		} else {
			candidate.Roots = append(candidate.Roots, root)
		}
		candidate.Version = qlsp.WorkspaceConfigVersion
		if err := candidate.Validate(m.lspDraftGlobal); err != nil {
			m.status = err.Error()
			return m, m.lspInputs[m.lspFormFocus].Focus()
		}
		m.lspDraftWorkspace = candidate
	}
	m.lspDiscardArmed = false
	m.cancelLSPForm()
	m.status = "Draft updated · ctrl+s to save"
	return m, nil
}

func (m *model) cancelLSPForm() {
	for index := range m.lspInputs {
		m.lspInputs[index].Blur()
	}
	m.lspMode = lspModeList
	m.lspFormFocus = 0
	m.lspEditingIndex = -1
	m.lspEditID = ""
}

func (m *model) focusLSPForm() tea.Cmd {
	for index := range m.lspInputs {
		m.lspInputs[index].Blur()
	}
	return m.lspInputs[m.lspFormFocus].Focus()
}

func (m model) deleteLSPEntry() (tea.Model, tea.Cmd) {
	if m.lspPanel == 0 {
		ids := m.lspServerIDs()
		if len(ids) == 0 {
			return m, nil
		}
		id := ids[min(m.lspCursor[0], len(ids)-1)]
		for _, root := range m.lspDraftWorkspace.Roots {
			if root.Server == id {
				m.status = "Server is selected by root " + root.Path + " · edit the root first"
				return m, nil
			}
		}
		delete(m.lspDraftGlobal.Servers, id)
		for language, serverID := range m.lspDraftGlobal.Languages {
			if serverID == id {
				delete(m.lspDraftGlobal.Languages, language)
			}
		}
		if m.lspCursor[0] >= len(m.lspDraftGlobal.Servers) {
			m.lspCursor[0] = max(0, len(m.lspDraftGlobal.Servers)-1)
		}
		m.status = "Server removed from draft · ctrl+s to save"
	} else if len(m.lspDraftWorkspace.Roots) > 0 {
		index := min(m.lspCursor[1], len(m.lspDraftWorkspace.Roots)-1)
		m.lspDraftWorkspace.Roots = append(m.lspDraftWorkspace.Roots[:index], m.lspDraftWorkspace.Roots[index+1:]...)
		if m.lspCursor[1] >= len(m.lspDraftWorkspace.Roots) {
			m.lspCursor[1] = max(0, len(m.lspDraftWorkspace.Roots)-1)
		}
		m.status = "Root removed from draft · ctrl+s to save"
	}
	m.lspDiscardArmed = false
	return m, nil
}

func (m model) toggleLSPEntry() (tea.Model, tea.Cmd) {
	if m.lspPanel == 0 {
		ids := m.lspServerIDs()
		if len(ids) == 0 {
			return m, nil
		}
		id := ids[min(m.lspCursor[0], len(ids)-1)]
		server := m.lspDraftGlobal.Servers[id]
		server.Disabled = !server.Disabled
		m.lspDraftGlobal.Servers[id] = server
	} else if len(m.lspDraftWorkspace.Roots) > 0 {
		index := min(m.lspCursor[1], len(m.lspDraftWorkspace.Roots)-1)
		m.lspDraftWorkspace.Roots[index].Disabled = !m.lspDraftWorkspace.Roots[index].Disabled
	}
	m.lspDiscardArmed = false
	m.status = "Enabled state changed · ctrl+s to save"
	return m, nil
}

func (m model) makeLSPServerDefault() (tea.Model, tea.Cmd) {
	ids := m.lspServerIDs()
	if len(ids) == 0 {
		return m, nil
	}
	id := ids[min(m.lspCursor[0], len(ids)-1)]
	server := m.lspDraftGlobal.Servers[id]
	if m.lspDraftGlobal.Languages == nil {
		m.lspDraftGlobal.Languages = make(map[string]string)
	}
	for _, language := range server.Languages {
		m.lspDraftGlobal.Languages[strings.ToLower(language)] = id
	}
	m.lspDiscardArmed = false
	m.status = "Selected server is now the global default for its languages · ctrl+s to save"
	return m, nil
}

func (m model) startLSPDiscovery() (tea.Model, tea.Cmd) {
	if m.workspaceStore == nil || m.lspBusy {
		return m, nil
	}
	m.lspBusy = true
	m.lspDiscoveryID++
	m.status = "Discovering language project roots…"
	store := *m.workspaceStore
	discoveryID := m.lspDiscoveryID
	scanRoots := m.lspPanel == 1
	existingRoots := append([]qlsp.RootConfig(nil), m.lspDraftWorkspace.Roots...)
	return m, func() tea.Msg {
		if !scanRoots {
			return lspDiscoveryMsg{id: discoveryID, servers: qlsp.DiscoverServers(nil)}
		}
		roots, err := store.DiscoverLSPRoots()
		if err != nil {
			return lspDiscoveryMsg{id: discoveryID, scanRoots: true, err: err}
		}
		languages := make([]string, 0, len(roots)+len(existingRoots))
		for _, root := range existingRoots {
			languages = append(languages, root.Language)
		}
		for _, root := range roots {
			languages = append(languages, root.Language)
		}
		return lspDiscoveryMsg{id: discoveryID, scanRoots: true, roots: roots, servers: qlsp.DiscoverServers(languages)}
	}
}

func (m model) applyLSPDiscovery(message lspDiscoveryMsg) (tea.Model, tea.Cmd) {
	if message.id != m.lspDiscoveryID || m.screen != screenLSP {
		return m, nil
	}
	m.lspBusy = false
	if message.err != nil {
		m.status = message.err.Error()
		return m, nil
	}
	discovered := message.roots
	seen := make(map[string]struct{}, len(m.lspDraftWorkspace.Roots))
	for _, root := range m.lspDraftWorkspace.Roots {
		seen[lspRootKey(root)] = struct{}{}
	}
	addedRoots := 0
	if message.scanRoots {
		for _, root := range discovered {
			if _, exists := seen[lspRootKey(root)]; exists {
				continue
			}
			m.lspDraftWorkspace.Roots = append(m.lspDraftWorkspace.Roots, root)
			seen[lspRootKey(root)] = struct{}{}
			addedRoots++
		}
	}
	sort.Slice(m.lspDraftWorkspace.Roots, func(i, j int) bool {
		if m.lspDraftWorkspace.Roots[i].Path == m.lspDraftWorkspace.Roots[j].Path {
			return m.lspDraftWorkspace.Roots[i].Language < m.lspDraftWorkspace.Roots[j].Language
		}
		return m.lspDraftWorkspace.Roots[i].Path < m.lspDraftWorkspace.Roots[j].Path
	})
	m.lspDiscardArmed = false
	addedServers := m.mergeDiscoveredLSPServers(message.servers)
	if message.scanRoots {
		m.status = fmt.Sprintf("Discovery found %d roots and %d installed servers · %d roots and %d servers added to draft", len(discovered), len(message.servers), addedRoots, addedServers)
	} else {
		m.status = fmt.Sprintf("Discovery found %d installed servers · %d added to draft", len(message.servers), addedServers)
	}
	return m, nil
}

func (m *model) mergeDiscoveredLSPServers(discovered []qlsp.DiscoveredServer) int {
	if m.lspDraftGlobal.Servers == nil {
		m.lspDraftGlobal.Servers = make(map[string]qlsp.ServerConfig)
	}
	if m.lspDraftGlobal.Languages == nil {
		m.lspDraftGlobal.Languages = make(map[string]string)
	}
	added := 0
	for _, found := range discovered {
		if _, exists := m.lspDraftGlobal.Servers[found.ID]; exists || hasLSPCommand(m.lspDraftGlobal, found.Config.Command) {
			continue
		}
		m.lspDraftGlobal.Servers[found.ID] = found.Config
		for _, language := range found.Config.Languages {
			language = strings.ToLower(language)
			if m.lspDraftGlobal.Languages[language] == "" {
				m.lspDraftGlobal.Languages[language] = found.ID
			}
		}
		added++
	}
	return added
}

func hasLSPCommand(global qlsp.GlobalConfig, command string) bool {
	for _, server := range global.Servers {
		if strings.EqualFold(strings.TrimSpace(server.Command), strings.TrimSpace(command)) {
			return true
		}
	}
	return false
}

func (m model) saveLSPSettings() (tea.Model, tea.Cmd) {
	if m.workspaceStore == nil {
		m.status = "Workspace storage is unavailable"
		return m, nil
	}
	if err := m.lspDraftGlobal.Validate(); err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.lspDraftWorkspace.Version = qlsp.WorkspaceConfigVersion
	if err := m.lspDraftWorkspace.Validate(m.lspDraftGlobal); err != nil {
		m.status = err.Error()
		return m, nil
	}
	updated := m.config
	updated.LSP = cloneLSPGlobal(m.lspDraftGlobal)
	if err := m.store.Save(updated); err != nil {
		m.status = err.Error()
		return m, nil
	}
	if err := m.workspaceStore.SaveLSP(m.lspDraftWorkspace, m.lspDraftGlobal); err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.config = updated
	m.lspOriginalGlobal = cloneLSPGlobal(m.lspDraftGlobal)
	m.lspOriginalWorkspace = cloneLSPWorkspace(m.lspDraftWorkspace)
	m.lspDiscardArmed = false
	m.status = "LSP settings saved · runtime activation is not enabled yet"
	return m, nil
}

func (m model) viewLSP() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · Language servers"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render("global servers · " + filepath.Clean(m.store.Path())))
	body.WriteString("\n")
	if m.workspaceStore != nil {
		body.WriteString(subtleStyle.Render("workspace roots · " + filepath.Clean(m.workspaceStore.LSPPath())))
		body.WriteString("\n")
	}
	body.WriteString("\n")
	if m.lspMode != lspModeList {
		body.WriteString(m.viewLSPForm())
	} else {
		body.WriteString(m.viewLSPLists())
	}
	if m.status != "" {
		body.WriteString("\n\n")
		body.WriteString(subtleStyle.Render(m.status))
	}
	body.WriteString("\n\n")
	help := "tab/←/→ panel · ↑/↓ select · a add · e/enter edit · d delete · space enable · m language default · r auto-detect · ctrl+s save · esc chat"
	if m.lspMode != lspModeList {
		help = "tab/↑/↓ field · enter/ctrl+s apply draft · esc cancel"
	} else if m.isStandaloneScreen(screenLSP) {
		help = strings.TrimSuffix(help, "esc chat") + "esc quit"
	}
	body.WriteString(helpStyle.Render(help))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewLSPLists() string {
	width := max(36, m.width-4)
	gap := 2
	panelWidth := max(16, (width-gap)/2)
	serverTitle := "GLOBAL SERVERS"
	rootTitle := "WORKSPACE ROOTS"
	if m.lspPanel == 0 {
		serverTitle = "› " + serverTitle
	} else {
		rootTitle = "› " + rootTitle
	}
	var servers strings.Builder
	servers.WriteString(agentTraceTitleStyle(m.dark).Render(serverTitle))
	servers.WriteString("\n")
	ids := m.lspServerIDs()
	if len(ids) == 0 {
		servers.WriteString(subtleStyle.Render("  No server profiles · press a"))
	}
	serverStart, serverEnd := lspVisibleRange(len(ids), m.lspCursor[0], max(2, (m.height-14)/2))
	if serverStart > 0 {
		servers.WriteString(subtleStyle.Render(fmt.Sprintf("  … %d above\n", serverStart)))
	}
	for index := serverStart; index < serverEnd; index++ {
		id := ids[index]
		server := m.lspDraftGlobal.Servers[id]
		cursor := "  "
		if m.lspPanel == 0 && index == m.lspCursor[0] {
			cursor = "› "
		}
		state := "on"
		if server.Disabled {
			state = "off"
		}
		servers.WriteString(fmt.Sprintf("%s%s  [%s]\n", cursor, id, state))
		languages := make([]string, 0, len(server.Languages))
		for _, language := range server.Languages {
			if m.lspDraftGlobal.Languages[language] == id {
				language += "*"
			}
			languages = append(languages, language)
		}
		servers.WriteString(subtleStyle.Render("    " + strings.Join(languages, ", ") + " · " + server.Command + " " + strings.Join(server.Args, " ")))
		servers.WriteString("\n")
	}
	if serverEnd < len(ids) {
		servers.WriteString(subtleStyle.Render(fmt.Sprintf("  … %d below\n", len(ids)-serverEnd)))
	}
	var roots strings.Builder
	roots.WriteString(agentTraceTitleStyle(m.dark).Render(rootTitle))
	roots.WriteString("\n")
	if len(m.lspDraftWorkspace.Roots) == 0 {
		roots.WriteString(subtleStyle.Render("  No roots · press a or r"))
	}
	rootStart, rootEnd := lspVisibleRange(len(m.lspDraftWorkspace.Roots), m.lspCursor[1], max(2, (m.height-14)/2))
	if rootStart > 0 {
		roots.WriteString(subtleStyle.Render(fmt.Sprintf("  … %d above\n", rootStart)))
	}
	for index := rootStart; index < rootEnd; index++ {
		root := m.lspDraftWorkspace.Roots[index]
		cursor := "  "
		if m.lspPanel == 1 && index == m.lspCursor[1] {
			cursor = "› "
		}
		state := "on"
		if root.Disabled {
			state = "off"
		}
		server, ok := qlsp.ResolveServer(m.lspDraftGlobal, root)
		if !ok {
			server = "unresolved"
		}
		roots.WriteString(fmt.Sprintf("%s%s  [%s]\n", cursor, root.Path, state))
		roots.WriteString(subtleStyle.Render("    " + root.Language + " · " + server + " · " + root.Source))
		roots.WriteString("\n")
	}
	if rootEnd < len(m.lspDraftWorkspace.Roots) {
		roots.WriteString(subtleStyle.Render(fmt.Sprintf("  … %d below\n", len(m.lspDraftWorkspace.Roots)-rootEnd)))
	}
	panel := lipgloss.NewStyle().Width(panelWidth)
	return lipgloss.JoinHorizontal(lipgloss.Top, panel.Render(servers.String()), strings.Repeat(" ", gap), panel.Render(roots.String()))
}

func (m model) viewLSPForm() string {
	labels := []string{"Profile ID", "Languages (comma-separated)", "Command", "Arguments (JSON array)"}
	title := "SERVER PROFILE"
	count := 4
	if m.lspMode == lspModeEditRoot {
		title = "PROJECT ROOT"
		labels = []string{"Workspace-relative path", "Language", "Server override (optional)"}
		count = 3
	}
	var body strings.Builder
	body.WriteString(agentTraceTitleStyle(m.dark).Render(title))
	body.WriteString("\n")
	for index := range count {
		prefix := "  "
		if index == m.lspFormFocus {
			prefix = "› "
		}
		body.WriteString(activeLabelStyle.Render(prefix + labels[index]))
		body.WriteString("\n  ")
		body.WriteString(m.lspInputs[index].View())
		body.WriteString("\n")
	}
	return body.String()
}

func (m model) lspServerIDs() []string {
	ids := make([]string, 0, len(m.lspDraftGlobal.Servers))
	for id := range m.lspDraftGlobal.Servers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func (m model) lspModified() bool {
	return !lspGlobalEqual(m.lspDraftGlobal, m.lspOriginalGlobal) || !lspWorkspaceEqual(m.lspDraftWorkspace, m.lspOriginalWorkspace)
}
func lspRootKey(root qlsp.RootConfig) string {
	return strings.ToLower(filepath.ToSlash(filepath.Clean(root.Path))) + "\x00" + strings.ToLower(root.Language)
}
func cloneLSPGlobal(value qlsp.GlobalConfig) qlsp.GlobalConfig {
	result := qlsp.GlobalConfig{Servers: make(map[string]qlsp.ServerConfig, len(value.Servers)), Languages: make(map[string]string, len(value.Languages))}
	for id, server := range value.Servers {
		server.Languages = append([]string(nil), server.Languages...)
		server.Args = append([]string(nil), server.Args...)
		result.Servers[id] = server
	}
	for language, server := range value.Languages {
		result.Languages[language] = server
	}
	return result
}
func cloneLSPWorkspace(value qlsp.WorkspaceConfig) qlsp.WorkspaceConfig {
	value.Roots = append([]qlsp.RootConfig(nil), value.Roots...)
	return value
}
func lspGlobalEqual(a, b qlsp.GlobalConfig) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}
func lspWorkspaceEqual(a, b qlsp.WorkspaceConfig) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}
func containsStringFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func lspVisibleRange(length, cursor, maximum int) (int, int) {
	if length <= maximum {
		return 0, length
	}
	maximum = max(1, maximum)
	start := max(0, cursor-maximum/2)
	if start+maximum > length {
		start = length - maximum
	}
	return start, start + maximum
}

// RunLSP opens lightweight global server and workspace root settings without
// starting Gateway, Session Store, Loom, or the agent tool runtime.
func RunLSP(ctx context.Context, globalStore config.Store, workspaceStore workspace.Store) error {
	lock, err := workspace.AcquireLock(workspaceStore.Root, "q lsp")
	if err != nil {
		return err
	}
	defer lock.Close()
	loaded, err := globalStore.Load()
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			return errors.New("q lsp requires an existing q configuration; run q once first")
		}
		return err
	}
	m := newModel(ctx, globalStore, nil)
	m.config = loaded
	m.workspaceStore = &workspaceStore
	updated, _ := m.enterLSP()
	m = updated.(model)
	if m.screen != screenLSP {
		return errors.New(m.status)
	}
	_, err = runStandalone(m, screenLSP)
	return err
}

func RunLSPDefault(ctx context.Context) error {
	globalStore, err := config.DefaultStore()
	if err != nil {
		return err
	}
	workspaceStore, err := workspace.DefaultStore()
	if err != nil {
		return err
	}
	if err := RunLSP(ctx, globalStore, workspaceStore); err != nil {
		return fmt.Errorf("q lsp: %w", err)
	}
	return nil
}
