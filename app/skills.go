package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/agentskills"
)

type skillScreenMode uint8

const (
	skillModeBrowse skillScreenMode = iota
	skillModeAdd
	skillModeRemove
)

const (
	skillScopeGlobal = iota
	skillScopeWorkspace
	skillScopeCount
)

type skillRuntime interface {
	Skills() []agentskills.Skill
	SkillEntries() []agentskills.Skill
	SkillIssues() []agentskills.Issue
	ReloadSkills() error
}

type skillManageRuntime interface {
	skillRuntime
	InstallSkill(context.Context, string, string) (agentskills.Skill, error)
	UpdateSkill(context.Context, string) (agentskills.Skill, error)
	RemoveSkill(context.Context, string) (agentskills.Skill, error)
}

type skillActionMsg struct {
	detail string
	err    error
}

// startSkillCommand retains the original command form for scripts and muscle
// memory. The normal management path is now the interactive /skills screen.
func (m model) startSkillCommand(content string) (model, tea.Cmd, bool) {
	parts := strings.Fields(content)
	if len(parts) < 2 || parts[0] != "/skills" || parts[1] == "" {
		return m, nil, false
	}
	runtime, ok := m.currentSkillRuntime().(skillManageRuntime)
	if !ok {
		m.status = "Agent Skills Git management is unavailable"
		return m, nil, true
	}
	switch parts[1] {
	case "add":
		if len(parts) != 4 {
			m.status = "usage: /skills add <global|workspace> <git-url>"
			return m, nil, true
		}
		scope, repository := parts[2], parts[3]
		m.input.Reset()
		m.skillsBusy = true
		m.status = "Cloning Agent Skill…"
		return m, installSkillCmd(m.ctx, runtime, scope, repository), true
	case "update":
		if len(parts) != 3 {
			m.status = "usage: /skills update <skill-id|name>"
			return m, nil, true
		}
		m.input.Reset()
		m.skillsBusy = true
		m.status = "Updating Agent Skill…"
		return m, updateSkillCmd(m.ctx, runtime, parts[2]), true
	case "remove":
		if len(parts) != 3 {
			m.status = "usage: /skills remove <skill-id|name>"
			return m, nil, true
		}
		m.input.Reset()
		m.skillsBusy = true
		m.status = "Removing Git-managed Agent Skill…"
		return m, removeSkillCmd(m.ctx, runtime, parts[2]), true
	default:
		m.status = "Open /skills for interactive management"
		return m, nil, true
	}
}

func installSkillCmd(ctx context.Context, runtime skillManageRuntime, scope, repository string) tea.Cmd {
	return func() tea.Msg {
		skill, err := runtime.InstallSkill(ctx, scope, repository)
		return skillActionMsg{detail: fmt.Sprintf("Installed %s · %s", skill.Name, skillScopeDisplayName(skill.Scope)), err: err}
	}
}

func updateSkillCmd(ctx context.Context, runtime skillManageRuntime, identifier string) tea.Cmd {
	return func() tea.Msg {
		skill, err := runtime.UpdateSkill(ctx, identifier)
		return skillActionMsg{detail: fmt.Sprintf("Pulled %s · %s", skill.Name, skillScopeDisplayName(skill.Scope)), err: err}
	}
}

func removeSkillCmd(ctx context.Context, runtime skillManageRuntime, identifier string) tea.Cmd {
	return func() tea.Msg {
		skill, err := runtime.RemoveSkill(ctx, identifier)
		return skillActionMsg{detail: fmt.Sprintf("Removed %s · %s", skill.Name, skillScopeDisplayName(skill.Scope)), err: err}
	}
}

func reloadSkillsCmd(runtime skillRuntime) tea.Cmd {
	return func() tea.Msg {
		err := runtime.ReloadSkills()
		return skillActionMsg{detail: fmt.Sprintf("Skill index reloaded · %d active", len(runtime.Skills())), err: err}
	}
}

func (m model) enterSkills() (tea.Model, tea.Cmd) {
	if m.currentSkillRuntime() == nil {
		m.status = "Agent Skills registry is unavailable"
		return m, m.input.Focus()
	}
	m.screen = screenSkills
	m.input.Blur()
	m.skillsInput.Blur()
	m.skillsMode = skillModeBrowse
	m.status = ""
	m.skillsStatusError = false
	m.resize(m.width, m.height)
	m.refreshSkills(true)
	if len(m.skillsForScope(skillScopeGlobal)) == 0 && len(m.skillsForScope(skillScopeWorkspace)) > 0 {
		m.skillsScope = skillScopeWorkspace
	}
	return m, nil
}

func (m model) updateSkills(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.skillsBusy {
		return m, nil
	}
	if m.skillsMode == skillModeAdd {
		switch key.String() {
		case "esc":
			m.skillsMode = skillModeBrowse
			m.skillsInput.Blur()
			m.skillsInput.Reset()
			m.status = ""
			return m, nil
		case "enter":
			repository := strings.TrimSpace(m.skillsInput.Value())
			if repository == "" {
				m.status = "Enter a Git repository URL"
				return m, nil
			}
			runtime, ok := m.currentSkillRuntime().(skillManageRuntime)
			if !ok {
				m.status = "Agent Skills Git management is unavailable"
				return m, nil
			}
			scope := skillScopeName(m.skillsScope)
			m.skillsBusy = true
			m.skillsMode = skillModeBrowse
			m.skillsInput.Blur()
			m.skillsInput.Reset()
			m.status = "Cloning into " + skillScopeLabel(m.skillsScope) + "…"
			m.skillsStatusError = false
			return m, installSkillCmd(m.ctx, runtime, scope, repository)
		}
		var command tea.Cmd
		m.skillsInput, command = m.skillsInput.Update(key)
		return m, command
	}

	if m.skillsMode == skillModeRemove {
		switch key.String() {
		case "y", "enter":
			skill, ok := m.selectedSkill()
			if !ok {
				m.skillsMode = skillModeBrowse
				return m, nil
			}
			runtime, ok := m.currentSkillRuntime().(skillManageRuntime)
			if !ok {
				m.status = "Agent Skills Git management is unavailable"
				return m, nil
			}
			m.skillsMode = skillModeBrowse
			m.skillsBusy = true
			m.status = "Removing " + skill.Name + "…"
			m.skillsStatusError = false
			return m, removeSkillCmd(m.ctx, runtime, skill.ID)
		case "n", "esc":
			m.skillsMode = skillModeBrowse
			m.status = "Removal cancelled"
			return m, nil
		default:
			return m, nil
		}
	}

	switch key.String() {
	case "esc":
		m.screen = screenChat
		m.status = ""
		if m.isStandaloneScreen(screenSkills) {
			return m, tea.Quit
		}
		return m, m.input.Focus()
	case "tab":
		m.skillsScope = (m.skillsScope + 1) % skillScopeCount
		m.refreshSkills(false)
		return m, nil
	case "left", "h":
		m.skillsScope = skillScopeGlobal
		m.refreshSkills(false)
		return m, nil
	case "right", "l":
		m.skillsScope = skillScopeWorkspace
		m.refreshSkills(false)
		return m, nil
	case "up", "k":
		if m.skillsCursor[m.skillsScope] > 0 {
			m.skillsCursor[m.skillsScope]--
		}
		return m, nil
	case "down", "j":
		count := len(m.skillsForScope(m.skillsScope))
		if m.skillsCursor[m.skillsScope]+1 < count {
			m.skillsCursor[m.skillsScope]++
		}
		return m, nil
	case "home":
		m.skillsCursor[m.skillsScope] = 0
		return m, nil
	case "end":
		if count := len(m.skillsForScope(m.skillsScope)); count > 0 {
			m.skillsCursor[m.skillsScope] = count - 1
		}
		return m, nil
	case "pgup":
		m.skillsCursor[m.skillsScope] = max(0, m.skillsCursor[m.skillsScope]-8)
		return m, nil
	case "pgdown":
		count := len(m.skillsForScope(m.skillsScope))
		if count > 0 {
			m.skillsCursor[m.skillsScope] = min(count-1, m.skillsCursor[m.skillsScope]+8)
		}
		return m, nil
	case "a":
		m.skillsMode = skillModeAdd
		m.skillsInput.Reset()
		m.status = "Add a Git skill to " + skillScopeLabel(m.skillsScope)
		return m, m.skillsInput.Focus()
	case "u":
		return m.startSelectedSkillUpdate()
	case "d", "delete":
		skill, ok := m.selectedSkill()
		if !ok {
			m.status = "No skill selected"
			return m, nil
		}
		if !skillIsManagedGit(skill) {
			m.status = "Read-only discovery entry · manage it in " + filepath.Clean(skill.Directory)
			return m, nil
		}
		m.skillsMode = skillModeRemove
		m.status = fmt.Sprintf("Delete %s and its checkout?", skill.Name)
		return m, nil
	case "r":
		runtime := m.currentSkillRuntime()
		if runtime == nil {
			m.status = "Agent Skills registry is unavailable"
			return m, nil
		}
		m.skillsBusy = true
		m.status = "Reloading skill registry…"
		m.skillsStatusError = false
		return m, reloadSkillsCmd(runtime)
	}
	return m, nil
}

func (m model) startSelectedSkillUpdate() (tea.Model, tea.Cmd) {
	skill, ok := m.selectedSkill()
	if !ok {
		m.status = "No skill selected"
		return m, nil
	}
	if !skillIsManagedGit(skill) {
		m.status = "Read-only discovery entry · only q-managed Git skills can be pulled"
		return m, nil
	}
	runtime, ok := m.currentSkillRuntime().(skillManageRuntime)
	if !ok {
		m.status = "Agent Skills Git management is unavailable"
		return m, nil
	}
	m.skillsBusy = true
	m.status = "Pulling " + skill.Name + "…"
	m.skillsStatusError = false
	return m, updateSkillCmd(m.ctx, runtime, skill.ID)
}

func (m *model) refreshSkills(gotoTop bool) {
	for scope := 0; scope < skillScopeCount; scope++ {
		count := len(m.skillsForScope(scope))
		if gotoTop || count == 0 {
			m.skillsCursor[scope] = 0
		} else if m.skillsCursor[scope] >= count {
			m.skillsCursor[scope] = count - 1
		}
	}
}

func (m model) skillEntries() []agentskills.Skill {
	runtime := m.currentSkillRuntime()
	if runtime == nil {
		return nil
	}
	return runtime.SkillEntries()
}

func (m model) skillsForScope(scope int) []agentskills.Skill {
	wanted := skillStoredScopeName(scope)
	var result []agentskills.Skill
	for _, skill := range m.skillEntries() {
		if skill.Scope == wanted {
			result = append(result, skill)
		}
	}
	return result
}

func (m model) selectedSkill() (agentskills.Skill, bool) {
	skills := m.skillsForScope(m.skillsScope)
	if len(skills) == 0 {
		return agentskills.Skill{}, false
	}
	cursor := min(max(0, m.skillsCursor[m.skillsScope]), len(skills)-1)
	return skills[cursor], true
}

func (m model) activeSkillIDs() map[string]bool {
	active := make(map[string]bool)
	if runtime := m.currentSkillRuntime(); runtime != nil {
		for _, skill := range runtime.Skills() {
			active[skill.ID] = true
		}
	}
	return active
}

func skillScopeName(scope int) string {
	if scope == skillScopeWorkspace {
		return "workspace"
	}
	return "global"
}

func skillStoredScopeName(scope int) string {
	if scope == skillScopeWorkspace {
		return "project"
	}
	return "global"
}

func skillScopeLabel(scope int) string {
	if scope == skillScopeWorkspace {
		return "WORKSPACE"
	}
	return "GLOBAL"
}

func skillScopeDisplayName(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "project", "session", "workspace":
		return "workspace"
	default:
		return scope
	}
}

func skillIsManagedGit(skill agentskills.Skill) bool {
	if skill.Source != agentskills.SourceUserQ && skill.Source != agentskills.SourceProjectQ {
		return false
	}
	_, err := os.Stat(filepath.Join(skill.Directory, ".git"))
	return err == nil
}

func (m model) renderSkillListPanel(scope, width, height int, active map[string]bool) string {
	skills := m.skillsForScope(scope)
	focused := m.skillsScope == scope
	title := fmt.Sprintf("%s SKILLS · %d", skillScopeLabel(scope), len(skills))
	if focused {
		title = "› " + title
	}
	listHeight := max(2, height-4)
	cursor := min(max(0, m.skillsCursor[scope]), max(0, len(skills)-1))
	start := 0
	if cursor >= listHeight {
		start = cursor - listHeight + 1
	}
	end := min(len(skills), start+listHeight)
	var body strings.Builder
	if focused {
		body.WriteString(activeLabelStyle.Render(title))
	} else {
		body.WriteString(titleStyle.Render(title))
	}
	body.WriteString("\n")
	if focused {
		body.WriteString(helpStyle.Render("  a add · u pull · d delete"))
	} else {
		body.WriteString(helpStyle.Render("  tab/←/→ focus to manage"))
	}
	body.WriteString("\n")
	if len(skills) == 0 {
		body.WriteString(emptyStyle.Render("  No skills · press a to add"))
	}
	for index := start; index < end; index++ {
		skill := skills[index]
		prefix := "  "
		style := subtleStyle
		if focused && index == cursor {
			prefix = "› "
			style = activeLabelStyle
		}
		state := "external"
		if skillIsManagedGit(skill) {
			state = "git"
		}
		if !active[skill.ID] {
			state += ", shadowed"
		}
		available := max(8, width-len(prefix)-len(state)-6)
		name := ansi.Truncate(skill.Name, available, "…")
		body.WriteString(style.Render(prefix + name))
		body.WriteString(helpStyle.Render(" · " + state))
		if index+1 < end {
			body.WriteString("\n")
		}
	}
	if start > 0 || end < len(skills) {
		body.WriteString("\n")
		body.WriteString(helpStyle.Render(fmt.Sprintf("  %d–%d of %d", start+1, end, len(skills))))
	}
	border := themedColor(m.dark, "240", "245")
	if focused {
		border = themedColor(m.dark, "39", "33")
	}
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).BorderForeground(border).
		Padding(0, 1).Width(max(12, width-4)).Height(max(3, height-2)).
		Render(body.String())
}

func (m model) renderSelectedSkill(width int) string {
	skill, ok := m.selectedSkill()
	if !ok {
		return subtleStyle.Render("No skill selected. Press a to add a Git repository to this scope.")
	}
	state := "external · read-only"
	if skillIsManagedGit(skill) {
		state = "q-managed Git · update and delete available"
	}
	active := "active in search index"
	if !m.activeSkillIDs()[skill.ID] {
		active = "shadowed · excluded from search index"
	}
	var body strings.Builder
	body.WriteString(activeLabelStyle.Render(skill.Name))
	body.WriteString(subtleStyle.Render(" · " + state + " · " + active))
	body.WriteString("\n")
	body.WriteString(ansi.Truncate(strings.ReplaceAll(skill.Description, "\n", " "), max(20, width-2), "…"))
	body.WriteString("\n")
	body.WriteString(helpStyle.Render(filepath.Clean(skill.Directory)))
	return body.String()
}

func (m model) viewSkills() string {
	contentWidth := max(36, m.width-4)
	panelGap := 1
	panelWidth := max(18, (contentWidth-panelGap)/2)
	panelHeight := max(6, m.height-17)
	active := m.activeSkillIDs()
	left := m.renderSkillListPanel(skillScopeGlobal, panelWidth, panelHeight, active)
	right := m.renderSkillListPanel(skillScopeWorkspace, panelWidth, panelHeight, active)

	var body strings.Builder
	body.WriteString(titleStyle.Render("q · Agent Skills"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render("GLOBAL · user-level  |  WORKSPACE · current workspace  |  .agents entries are externally managed"))
	body.WriteString("\n\n")
	body.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", panelGap), right))
	body.WriteString("\n")
	body.WriteString(m.renderSelectedSkill(contentWidth))
	if runtime := m.currentSkillRuntime(); runtime != nil {
		if issues := runtime.SkillIssues(); len(issues) > 0 {
			body.WriteString("\n")
			body.WriteString(errorStyle.Render(fmt.Sprintf("DISCOVERY NOTES · %d · ", len(issues)) + issues[0].Message))
		}
	}

	if m.skillsMode == skillModeAdd {
		body.WriteString("\n\n")
		body.WriteString(activeLabelStyle.Render("ADD TO " + skillScopeLabel(m.skillsScope)))
		body.WriteString("\n")
		body.WriteString(m.skillsInput.View())
	} else if m.skillsMode == skillModeRemove {
		body.WriteString("\n\n")
		body.WriteString(errorStyle.Render(m.status + "  y/enter confirm · n/esc cancel"))
	} else if m.status != "" {
		body.WriteString("\n\n")
		style := subtleStyle
		if m.skillsStatusError {
			style = errorStyle
		}
		body.WriteString(style.Render(m.status))
	}

	body.WriteString("\n\n")
	if m.skillsBusy {
		body.WriteString(helpStyle.Render("working…"))
	} else if m.skillsMode == skillModeAdd {
		body.WriteString(helpStyle.Render("enter clone · esc cancel"))
	} else if m.skillsMode != skillModeRemove {
		help := "tab/←/→ scope · ↑/↓ select · a add · u pull · d delete · r reload · esc chat"
		if m.isStandaloneScreen(screenSkills) {
			help = "tab/←/→ scope · ↑/↓ select · a add · u pull · d delete · r reload · esc quit"
		}
		body.WriteString(helpStyle.Render(help))
	}
	return frameStyle.Width(contentWidth).Render(body.String())
}

func (m model) currentSkillRuntime() skillRuntime {
	if m.skillRegistry != nil {
		return m.skillRegistry
	}
	runtime, _ := m.toolRuntime.(skillRuntime)
	return runtime
}
