package app

import (
	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (m model) updateModelPicker(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.discovering {
		return m, nil
	}
	switch m.modelPickerStage {
	case modelPickerTargets:
		return m.updateModelTargetPicker(key)
	case modelPickerRoleName:
		return m.updateModelRoleName(key)
	case modelPickerGroups:
		return m.updateModelGroups(key)
	case modelPickerGroupName:
		return m.updateModelGroupName(key)
	case modelPickerGroupCandidates:
		return m.updateModelGroupCandidates(key)
	case modelPickerGroupCandidateModels:
		return m.updateModelGroupCandidateModels(key)
	case modelPickerGroupCandidateReasoning:
		return m.updateModelGroupCandidateReasoning(key)
	case modelPickerGroupCandidateTimeout:
		return m.updateModelGroupCandidateTimeout(key)
	case modelPickerReasoning:
		return m.updateReasoningPicker(key)
	case modelPickerEmbeddingDimensions:
		return m.updateEmbeddingDimensionsPicker(key)
	case modelPickerContextWindow:
		return m.updateContextWindowPicker(key)
	}
	filtered := m.filteredModels()
	switch key.String() {
	case "esc":
		if m.modelChooseTarget {
			if !m.modelWorkspace && m.modelTarget == defaultModelTarget {
				m.draftConfig.Provider = m.config.Provider
			}
			m.modelPickerStage = modelPickerTargets
			m.modelFilter.Blur()
			m.status = ""
			return m, nil
		}
		if m.runtime != nil && m.modelReturn == screenChat && !containsModel(filtered, m.activeConfig().Provider.Model) {
			m.status = "Select a model from the updated providers"
			return m, nil
		}
		m.screen = m.modelReturn
		m.status = ""
		m.modelFilter.Blur()
		if m.screen == screenChat {
			return m, m.input.Focus()
		}
		return m, m.setup[m.setupFocus].Focus()
	case "up":
		if m.modelCursor > 0 {
			m.modelCursor--
		}
		return m, nil
	case "down":
		if m.modelCursor < len(filtered)-1 {
			m.modelCursor++
		}
		return m, nil
	case "pgup":
		m.modelCursor = max(0, m.modelCursor-10)
		return m, nil
	case "pgdown":
		m.modelCursor = min(max(0, len(filtered)-1), m.modelCursor+10)
		return m, nil
	case "ctrl+e":
		if len(filtered) == 0 {
			return m, nil
		}
		if _, grouped := modelGroupChoice(m.draftConfig, filtered[m.modelCursor].ID); grouped {
			m.status = "Context overrides apply to models, not model groups"
			return m, nil
		}
		return m.enterContextWindowPicker(filtered[m.modelCursor])
	case "enter":
		if len(filtered) == 0 {
			return m, nil
		}
		value := m.draftConfig
		selected := filtered[m.modelCursor]
		if m.modelWorkspace {
			return m.saveWorkspaceModel(m.modelTarget, selected)
		}
		if m.modelTarget == embeddingModelTarget {
			value.Embedding.Model = selected.ID
			m.draftConfig = value
			m.modelSelection = selected
			m.modelPickerStage = modelPickerEmbeddingDimensions
			m.embeddingDimensions.SetValue("")
			if value.Embedding.Dimensions > 0 {
				m.embeddingDimensions.SetValue(strconv.Itoa(value.Embedding.Dimensions))
			}
			m.modelFilter.Blur()
			return m, m.embeddingDimensions.Focus()
		}
		if m.modelTarget == "" || m.modelTarget == defaultModelTarget {
			value.Provider.Model = selected.ID
			value.Provider.ContextWindow = selected.ContextLength
		} else if group, grouped := modelGroupChoice(value, selected.ID); grouped {
			value = withAgentGroup(value, m.modelTarget, group)
			if _, err := subagent.Resolve(value, m.modelTarget, m.models); err != nil {
				m.status = err.Error()
				return m, nil
			}
			return m.saveModelTargetConfiguration(value, m.modelTarget)
		} else {
			value = withAgentModel(value, m.modelTarget, selected.ID)
		}
		m.draftConfig = value
		m.modelSelection = selected
		reasoning := selectedReasoning(selected)
		if reasoning == nil || len(reasoning.SupportedEfforts) == 0 {
			value = withModelReasoningEffort(value, m.modelTarget, "")
			return m.saveModelTargetConfiguration(value, m.modelTarget)
		}
		m.modelPickerStage = modelPickerReasoning
		m.reasoningCursor = reasoningEffortCursor(value, m.modelTarget, reasoning.SupportedEfforts)
		m.modelFilter.Blur()
		return m, nil
	}
	var command tea.Cmd
	m.modelFilter, command = m.modelFilter.Update(key)
	m.modelCursor = 0
	return m, command
}

func (m model) updateModelTargetPicker(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	targets := m.modelTargets()
	if m.modelRoleDeleteArmed {
		switch key.String() {
		case "d", "y", "enter":
			m.modelRoleDeleteArmed = false
			if len(targets) == 0 {
				return m, nil
			}
			target := targets[min(m.modelTargetCursor, len(targets)-1)]
			if !config.ValidCustomRoleName(target) {
				m.status = "Only custom roles can be deleted"
				return m, nil
			}
			if refs := m.modelRoleReferences(target); len(refs) > 0 {
				m.status = "Change referencing subagents first: " + strings.Join(refs, ", ")
				return m, nil
			}
			value := m.draftConfig
			value.Agents.Roles = cloneAgentRoles(value.Agents.Roles)
			delete(value.Agents.Roles, target)
			return m.saveModelRoleConfiguration(value, target, "Custom role "+target+" deleted")
		case "n", "esc":
			m.modelRoleDeleteArmed = false
			m.status = "Delete canceled"
			return m, nil
		default:
			m.modelRoleDeleteArmed = false
			m.status = ""
		}
	}
	switch key.String() {
	case "esc":
		if m.isStandaloneScreen(screenModels) {
			return m, tea.Quit
		}
		m.screen = m.modelReturn
		m.status = ""
		if m.screen == screenChat {
			return m, m.input.Focus()
		}
		return m, m.setup[m.setupFocus].Focus()
	case "up":
		m.modelRoleDeleteArmed = false
		m.status = ""
		if m.modelTargetCursor > 0 {
			m.modelTargetCursor--
		}
	case "down":
		m.modelRoleDeleteArmed = false
		m.status = ""
		if m.modelTargetCursor < len(targets)-1 {
			m.modelTargetCursor++
		}
	case "left":
		m.modelRoleDeleteArmed = false
		m.status = ""
		m.modelScopeCursor = modelScopeGlobal
	case "right", "tab":
		m.modelRoleDeleteArmed = false
		m.status = ""
		m.modelScopeCursor = modelScopeWorkspace
	case "a":
		m.modelRoleDeleteArmed = false
		m.modelRoleNameInput.SetValue("")
		m.modelPickerStage = modelPickerRoleName
		m.status = ""
		m.modelFilter.Blur()
		return m, m.modelRoleNameInput.Focus()
	case "d":
		if len(targets) == 0 {
			return m, nil
		}
		target := targets[min(m.modelTargetCursor, len(targets)-1)]
		if !config.ValidCustomRoleName(target) {
			m.status = "Only custom roles can be deleted; press i to reset a model assignment"
			return m, nil
		}
		if refs := m.modelRoleReferences(target); len(refs) > 0 {
			m.status = "Change referencing subagents first: " + strings.Join(refs, ", ")
			return m, nil
		}
		m.modelRoleDeleteArmed = true
		m.status = "Delete custom role " + target + "?"
	case "g":
		m.modelPickerStage = modelPickerGroups
		m.modelGroupDeleteArmed = false
		m.status = ""
		m.selectModelGroup("")
		return m, nil
	case "enter":
		if len(targets) == 0 {
			return m, nil
		}
		m.modelTarget = targets[m.modelTargetCursor]
		m.modelWorkspace = m.modelScopeCursor == modelScopeWorkspace
		if m.modelWorkspace && !workspace.ModelOverrideAllowed(m.modelTarget) {
			m.status = modelTargetLabel(m.modelTarget) + " is shared globally and cannot be overridden per workspace"
			return m, nil
		}
		m.modelPickerStage = modelPickerModels
		m.modelFilter.Reset()
		m.selectConfiguredModel()
		return m, m.modelFilter.Focus()
	case "i":
		if len(targets) == 0 {
			return m, nil
		}
		target := targets[m.modelTargetCursor]
		if m.modelScopeCursor == modelScopeWorkspace {
			if !workspace.ModelOverrideAllowed(target) {
				m.status = modelTargetLabel(target) + " has no workspace override"
				return m, nil
			}
			return m.clearWorkspaceModel(target)
		}
		if target == defaultModelTarget {
			m.status = "The GLOBAL default model is required and cannot be reset"
			return m, nil
		}
		value := m.draftConfig
		if target == embeddingModelTarget {
			value.Embedding = config.EmbeddingConfig{}
		} else {
			value = withoutAgentOverride(value, target)
		}
		return m.saveModelTargetConfiguration(value, target)
	}
	return m, nil
}

func (m model) updateModelRoleName(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.modelRoleNameInput.Blur()
		m.modelPickerStage = modelPickerTargets
		m.status = ""
		return m, nil
	case "enter":
		name := strings.TrimSpace(m.modelRoleNameInput.Value())
		if !config.ValidCustomRoleName(name) {
			m.status = "Use 1–64 lowercase letters, digits or hyphens; built-in and reserved names are unavailable"
			return m, nil
		}
		if m.draftConfig.HasNativeRole(name) {
			m.status = "Role already exists"
			return m, nil
		}
		value := m.draftConfig
		value.Agents.Roles = cloneAgentRoles(value.Agents.Roles)
		value.Agents.Roles[name] = config.AgentConfig{}
		m.modelRoleNameInput.Blur()
		return m.saveModelRoleConfiguration(value, name, "Custom role "+name+" created")
	}
	var command tea.Cmd
	m.modelRoleNameInput, command = m.modelRoleNameInput.Update(key)
	return m, command
}

func (m model) modelRoleReferences(role string) []string {
	var refs []string
	for _, entry := range m.customStore().List() {
		if entry.Profile.Role != role {
			continue
		}
		name := entry.Profile.Name
		if name == "" {
			name = filepath.Base(entry.Path)
		}
		refs = append(refs, name+" ("+entry.Scope+")")
	}
	sort.Strings(refs)
	return refs
}

func (m model) updateModelGroups(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	names := modelGroupNames(m.draftConfig)
	if len(names) == 0 {
		m.modelGroupCursor = 0
	} else {
		m.modelGroupCursor = min(m.modelGroupCursor, len(names)-1)
	}
	switch key.String() {
	case "esc":
		m.modelPickerStage = modelPickerTargets
		m.status = ""
		m.modelGroupDeleteArmed = false
		return m, nil
	case "up":
		if m.modelGroupCursor > 0 {
			m.modelGroupCursor--
		}
		m.modelGroupDeleteArmed = false
	case "down":
		if m.modelGroupCursor < len(names)-1 {
			m.modelGroupCursor++
		}
		m.modelGroupDeleteArmed = false
	case "a":
		m.modelGroupName = ""
		m.modelGroupDraft = config.ModelGroupConfig{}
		m.modelGroupNameInput.SetValue("")
		m.modelPickerStage = modelPickerGroupName
		m.status = ""
		return m, m.modelGroupNameInput.Focus()
	case "enter":
		if len(names) == 0 {
			return m, nil
		}
		m.beginModelGroupEdit(names[m.modelGroupCursor])
		return m, nil
	case "d":
		if len(names) == 0 {
			return m, nil
		}
		name := names[m.modelGroupCursor]
		roles := rolesUsingModelGroup(m.draftConfig, name)
		for role, override := range m.workspaceModel.Overrides {
			if override.Model == "group/"+name {
				roles = append(roles, "workspace "+role)
			}
		}
		if len(roles) > 0 {
			m.status = "Group is used by: " + strings.Join(roles, ", ")
			m.modelGroupDeleteArmed = false
			return m, nil
		}
		if !m.modelGroupDeleteArmed {
			m.modelGroupDeleteArmed = true
			m.status = "Press d again to delete group " + name
			return m, nil
		}
		value := m.draftConfig
		value.ModelGroups = cloneModelGroups(value.ModelGroups)
		delete(value.ModelGroups, name)
		return m.saveModelGroupsConfiguration(value, "")
	}
	return m, nil
}

func (m model) updateModelGroupName(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.modelGroupNameInput.Blur()
		m.modelPickerStage = modelPickerGroups
		m.status = ""
		return m, nil
	case "enter":
		name := strings.TrimSpace(m.modelGroupNameInput.Value())
		if name == "" || name != m.modelGroupNameInput.Value() {
			m.status = "Group name must be non-empty without surrounding whitespace"
			return m, nil
		}
		if _, exists := m.draftConfig.ModelGroups[name]; exists {
			m.status = "Model group already exists: " + name
			return m, nil
		}
		m.modelGroupName = name
		m.modelGroupDraft = config.ModelGroupConfig{}
		m.modelGroupCandidateCursor = 0
		m.modelGroupNameInput.Blur()
		m.modelPickerStage = modelPickerGroupCandidates
		m.status = "Add at least one candidate"
		return m, nil
	}
	var command tea.Cmd
	m.modelGroupNameInput, command = m.modelGroupNameInput.Update(key)
	return m, command
}

func (m model) updateModelGroupCandidates(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	candidates := m.modelGroupDraft.Candidates
	if len(candidates) == 0 {
		m.modelGroupCandidateCursor = 0
	} else {
		m.modelGroupCandidateCursor = min(m.modelGroupCandidateCursor, len(candidates)-1)
	}
	switch key.String() {
	case "esc":
		if len(m.modelGroupDraft.Candidates) == 0 {
			if _, existing := m.draftConfig.ModelGroups[m.modelGroupName]; existing {
				m.status = "A model group requires at least one candidate"
				return m, nil
			}
			m.modelPickerStage = modelPickerGroups
			m.status = "Empty group discarded"
			return m, nil
		}
		return m.saveCurrentModelGroup()
	case "up":
		if m.modelGroupCandidateCursor > 0 {
			m.modelGroupCandidateCursor--
		}
	case "down":
		if m.modelGroupCandidateCursor < len(candidates)-1 {
			m.modelGroupCandidateCursor++
		}
	case "ctrl+up":
		index := m.modelGroupCandidateCursor
		if index > 0 {
			m.modelGroupDraft.Candidates[index-1], m.modelGroupDraft.Candidates[index] =
				m.modelGroupDraft.Candidates[index], m.modelGroupDraft.Candidates[index-1]
			m.modelGroupCandidateCursor--
		}
	case "ctrl+down":
		index := m.modelGroupCandidateCursor
		if index >= 0 && index < len(candidates)-1 {
			m.modelGroupDraft.Candidates[index+1], m.modelGroupDraft.Candidates[index] =
				m.modelGroupDraft.Candidates[index], m.modelGroupDraft.Candidates[index+1]
			m.modelGroupCandidateCursor++
		}
	case "a":
		return m.beginModelGroupCandidateEdit(-1)
	case "enter":
		if len(candidates) == 0 {
			return m, nil
		}
		return m.beginModelGroupCandidateEdit(m.modelGroupCandidateCursor)
	case "d":
		if len(candidates) == 0 {
			return m, nil
		}
		if len(candidates) == 1 {
			m.status = "A model group requires at least one candidate; delete the group from the previous screen instead"
			return m, nil
		}
		index := m.modelGroupCandidateCursor
		m.modelGroupDraft.Candidates = append(m.modelGroupDraft.Candidates[:index], m.modelGroupDraft.Candidates[index+1:]...)
		m.modelGroupCandidateCursor = min(index, len(m.modelGroupDraft.Candidates)-1)
		m.modelGroupCandidateCursor = max(0, m.modelGroupCandidateCursor)
	}
	return m, nil
}

func (m model) saveCurrentModelGroup() (tea.Model, tea.Cmd) {
	if err := subagent.ValidateModelGroup(m.modelGroupName, m.modelGroupDraft, m.models); err != nil {
		m.status = err.Error()
		return m, nil
	}
	value := m.draftConfig
	value.ModelGroups = cloneModelGroups(value.ModelGroups)
	value.ModelGroups[m.modelGroupName] = cloneModelGroup(m.modelGroupDraft)
	return m.saveModelGroupsConfiguration(value, m.modelGroupName)
}

func (m model) beginModelGroupCandidateEdit(index int) (tea.Model, tea.Cmd) {
	m.modelGroupCandidateEdit = index
	m.modelGroupCandidate = config.ModelCandidateConfig{}
	if index >= 0 && index < len(m.modelGroupDraft.Candidates) {
		m.modelGroupCandidate = m.modelGroupDraft.Candidates[index]
	}
	m.modelPickerStage = modelPickerGroupCandidateModels
	m.modelFilter.Reset()
	m.modelCursor = 0
	for candidateIndex, model := range m.candidateModels() {
		if model.ID == m.modelGroupCandidate.Model {
			m.modelCursor = candidateIndex
			break
		}
	}
	m.status = ""
	return m, m.modelFilter.Focus()
}

func (m model) updateModelGroupCandidateModels(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	filtered := m.filteredCandidateModels()
	switch key.String() {
	case "esc":
		m.modelFilter.Blur()
		m.modelPickerStage = modelPickerGroupCandidates
		return m, nil
	case "up":
		if m.modelCursor > 0 {
			m.modelCursor--
		}
		return m, nil
	case "down":
		if m.modelCursor < len(filtered)-1 {
			m.modelCursor++
		}
		return m, nil
	case "enter":
		if len(filtered) == 0 {
			return m, nil
		}
		m.modelSelection = filtered[m.modelCursor]
		m.modelGroupCandidate.Model = m.modelSelection.ID
		reasoning := selectedReasoning(m.modelSelection)
		m.modelFilter.Blur()
		if reasoning == nil || len(reasoning.SupportedEfforts) == 0 {
			m.modelGroupCandidate.ReasoningEffort = ""
			return m.enterModelGroupCandidateTimeout()
		}
		m.modelPickerStage = modelPickerGroupCandidateReasoning
		m.reasoningCursor = effortCursor(m.modelGroupCandidate.ReasoningEffort, reasoning.SupportedEfforts)
		return m, nil
	}
	var command tea.Cmd
	m.modelFilter, command = m.modelFilter.Update(key)
	m.modelCursor = 0
	return m, command
}

func (m model) updateModelGroupCandidateReasoning(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	reasoning := selectedReasoning(m.modelSelection)
	if reasoning == nil {
		m.modelPickerStage = modelPickerGroupCandidateModels
		return m, m.modelFilter.Focus()
	}
	options := append([]string{""}, reasoning.SupportedEfforts...)
	switch key.String() {
	case "esc":
		m.modelPickerStage = modelPickerGroupCandidateModels
		return m, m.modelFilter.Focus()
	case "up":
		if m.reasoningCursor > 0 {
			m.reasoningCursor--
		}
	case "down":
		if m.reasoningCursor < len(options)-1 {
			m.reasoningCursor++
		}
	case "enter":
		m.modelGroupCandidate.ReasoningEffort = options[m.reasoningCursor]
		return m.enterModelGroupCandidateTimeout()
	}
	return m, nil
}

func (m model) enterModelGroupCandidateTimeout() (tea.Model, tea.Cmd) {
	m.modelPickerStage = modelPickerGroupCandidateTimeout
	m.modelGroupTimeoutInput.SetValue("")
	if m.modelGroupCandidate.Timeout > 0 {
		m.modelGroupTimeoutInput.SetValue(m.modelGroupCandidate.Timeout.String())
	}
	return m, m.modelGroupTimeoutInput.Focus()
}

func (m model) updateModelGroupCandidateTimeout(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.modelGroupTimeoutInput.Blur()
		if selectedReasoning(m.modelSelection) != nil {
			m.modelPickerStage = modelPickerGroupCandidateReasoning
			return m, nil
		}
		m.modelPickerStage = modelPickerGroupCandidateModels
		return m, m.modelFilter.Focus()
	case "enter":
		raw := strings.TrimSpace(m.modelGroupTimeoutInput.Value())
		var timeout time.Duration
		if raw != "" {
			parsed, err := time.ParseDuration(raw)
			if err != nil || parsed <= 0 {
				m.status = "Timeout must be a positive duration such as 60s, or empty"
				return m, nil
			}
			timeout = parsed
		}
		m.modelGroupCandidate.Timeout = timeout
		for index, candidate := range m.modelGroupDraft.Candidates {
			if candidate.Model == m.modelGroupCandidate.Model && index != m.modelGroupCandidateEdit {
				m.status = "Model group already contains " + candidate.Model
				return m, nil
			}
		}
		if m.modelGroupCandidateEdit >= 0 && m.modelGroupCandidateEdit < len(m.modelGroupDraft.Candidates) {
			m.modelGroupDraft.Candidates[m.modelGroupCandidateEdit] = m.modelGroupCandidate
			m.modelGroupCandidateCursor = m.modelGroupCandidateEdit
		} else {
			m.modelGroupDraft.Candidates = append(m.modelGroupDraft.Candidates, m.modelGroupCandidate)
			m.modelGroupCandidateCursor = len(m.modelGroupDraft.Candidates) - 1
		}
		m.modelGroupTimeoutInput.Blur()
		m.modelPickerStage = modelPickerGroupCandidates
		m.status = "Candidate updated · changes save when leaving the group"
		return m, nil
	}
	var command tea.Cmd
	m.modelGroupTimeoutInput, command = m.modelGroupTimeoutInput.Update(key)
	return m, command
}

func (m *model) beginModelGroupEdit(name string) {
	m.modelGroupName = name
	m.modelGroupDraft = cloneModelGroup(m.draftConfig.ModelGroups[name])
	m.modelGroupCandidateCursor = 0
	m.modelPickerStage = modelPickerGroupCandidates
	m.modelGroupDeleteArmed = false
	m.status = ""
}

func (m *model) selectModelGroup(name string) {
	names := modelGroupNames(m.draftConfig)
	m.modelGroupCursor = 0
	for index, candidate := range names {
		if candidate == name {
			m.modelGroupCursor = index
			break
		}
	}
}

func (m *model) refreshModelGroupCatalog() {
	base := m.candidateModels()
	m.models = append([]client.Model(nil), base...)
	for _, name := range modelGroupNames(m.draftConfig) {
		group := m.draftConfig.ModelGroups[name]
		if !modelGroupAvailable(group, base) {
			continue
		}
		contextLength, output := modelGroupLimits(group, base)
		m.models = append(m.models, client.Model{
			ID: "group/" + name, Object: "model", OwnedBy: "q-model-group",
			ContextLength: contextLength, MaxOutputTokens: output,
		})
	}
	sort.Slice(m.models, func(i, j int) bool { return m.models[i].ID < m.models[j].ID })
}

func (m model) saveModelGroupsConfiguration(value config.Config, group string) (tea.Model, tea.Cmd) {
	if name, grouped := strings.CutPrefix(value.Provider.Model, "group/"); grouped {
		if configured, found := value.ModelGroups[name]; found {
			value.Provider.ContextWindow, _ = modelGroupLimits(configured, m.models)
		}
	}
	if err := value.Validate(); err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.status = "Saving model groups…"
	return m, func() tea.Msg {
		if err := m.store.Save(value); err != nil {
			return modelGroupsConfiguredMsg{err: err}
		}
		return modelGroupsConfiguredMsg{config: value, group: group}
	}
}

func modelGroupNames(value config.Config) []string {
	names := make([]string, 0, len(value.ModelGroups))
	for name := range value.ModelGroups {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func rolesUsingModelGroup(value config.Config, group string) []string {
	roles := make([]string, 0)
	if value.Provider.Model == "group/"+group {
		roles = append(roles, "default")
	}
	for role, agent := range value.Agents.Roles {
		if agent.Group == group || agent.Model == "group/"+group {
			roles = append(roles, role)
		}
	}
	sort.Strings(roles)
	return roles
}

func cloneModelGroups(source map[string]config.ModelGroupConfig) map[string]config.ModelGroupConfig {
	result := make(map[string]config.ModelGroupConfig, len(source)+1)
	for name, group := range source {
		result[name] = cloneModelGroup(group)
	}
	return result
}

func cloneModelGroup(group config.ModelGroupConfig) config.ModelGroupConfig {
	group.Candidates = append([]config.ModelCandidateConfig(nil), group.Candidates...)
	return group
}

func effortCursor(configured string, efforts []string) int {
	for index, effort := range efforts {
		if effort == configured {
			return index + 1
		}
	}
	return 0
}

func (m model) updateReasoningPicker(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	reasoning := selectedReasoning(m.modelSelection)
	if reasoning == nil || len(reasoning.SupportedEfforts) == 0 {
		m.modelPickerStage = modelPickerModels
		return m, m.modelFilter.Focus()
	}
	options := append([]string{""}, reasoning.SupportedEfforts...)
	switch key.String() {
	case "esc":
		m.modelPickerStage = modelPickerModels
		return m, m.modelFilter.Focus()
	case "up":
		if m.reasoningCursor > 0 {
			m.reasoningCursor--
		}
	case "down":
		if m.reasoningCursor < len(options)-1 {
			m.reasoningCursor++
		}
	case "enter":
		value := withModelReasoningEffort(m.draftConfig, m.modelTarget, options[m.reasoningCursor])
		return m.saveModelTargetConfiguration(value, m.modelTarget)
	}
	return m, nil
}

func (m model) updateEmbeddingDimensionsPicker(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.modelPickerStage = modelPickerModels
		m.embeddingDimensions.Blur()
		return m, m.modelFilter.Focus()
	case "enter":
		dimensions, err := strconv.Atoi(strings.TrimSpace(m.embeddingDimensions.Value()))
		if err != nil || dimensions < 1 || dimensions > 4096 {
			m.status = "Embedding dimensions must be a number between 1 and 4096"
			return m, nil
		}
		value := m.draftConfig
		value.Embedding.Dimensions = dimensions
		return m.saveModelTargetConfiguration(value, embeddingModelTarget)
	}
	var command tea.Cmd
	m.embeddingDimensions, command = m.embeddingDimensions.Update(key)
	return m, command
}
func (m model) enterContextWindowPicker(selected client.Model) (tea.Model, tea.Cmd) {
	if m.runtime == nil {
		m.status = "Gateway model metadata is unavailable for this provider"
		return m, nil
	}
	if _, _, found := gatewayModelLocation(m.gatewayConfig, selected.ID); !found {
		m.status = "Model is not mapped to a configured Gateway provider"
		return m, nil
	}
	m.modelSelection = selected
	m.modelPickerStage = modelPickerContextWindow
	m.modelContextWindow.SetValue("")
	if override, configured := m.gatewayContextWindowOverride(selected.ID); configured {
		m.modelContextWindow.SetValue(strconv.FormatInt(override, 10))
	}
	m.modelFilter.Blur()
	m.status = ""
	return m, m.modelContextWindow.Focus()
}

func (m model) updateContextWindowPicker(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.modelPickerStage = modelPickerModels
		m.modelContextWindow.Blur()
		m.status = ""
		return m, m.modelFilter.Focus()
	case "enter":
		raw := strings.TrimSpace(m.modelContextWindow.Value())
		var contextWindow int64
		if raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed <= 0 {
				m.status = "Context length must be a positive integer or empty"
				return m, nil
			}
			contextWindow = parsed
		}
		candidate := cloneGatewayConfig(m.gatewayConfig)
		providerIndex, upstreamID, found := gatewayModelLocation(candidate, m.modelSelection.ID)
		if !found {
			m.status = "Model is not mapped to a configured Gateway provider"
			return m, nil
		}
		provider := &candidate.Providers[providerIndex]
		if provider.ModelMetadata == nil {
			provider.ModelMetadata = make(map[string]client.ModelMetadata)
		}
		metadata := provider.ModelMetadata[upstreamID]
		metadata.ContextLength = contextWindow
		if metadata.ContextLength == 0 && metadata.MaxOutputTokens == 0 && metadata.Capabilities == nil {
			delete(provider.ModelMetadata, upstreamID)
		} else {
			provider.ModelMetadata[upstreamID] = metadata
		}
		if len(provider.ModelMetadata) == 0 {
			provider.ModelMetadata = nil
		}
		return m.applyGatewayConfig(candidate)
	}
	var command tea.Cmd
	m.modelContextWindow, command = m.modelContextWindow.Update(key)
	return m, command
}

func (m model) saveConfiguration(value config.Config, preserveHistory bool) (tea.Model, tea.Cmd) {
	if err := value.Validate(); err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.status = "Saving configuration…"
	m.modelFilter.Blur()
	return m, func() tea.Msg {
		if err := m.store.Save(value); err != nil {
			return configuredMsg{err: err}
		}
		configuredClient, err := m.factory(value)
		return configuredMsg{config: value, client: configuredClient, preserveHistory: preserveHistory, err: err}
	}
}

func (m model) saveModelTargetConfiguration(value config.Config, target string) (tea.Model, tea.Cmd) {
	if target == "" || target == defaultModelTarget {
		return m.saveConfiguration(value, m.modelReturn == screenChat)
	}
	if err := value.Validate(); err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.status = "Saving " + target + " model settings…"
	m.modelFilter.Blur()
	m.embeddingDimensions.Blur()
	return m, func() tea.Msg {
		if err := m.store.Save(value); err != nil {
			return modelTargetConfiguredMsg{err: err}
		}
		return modelTargetConfiguredMsg{config: value, target: target}
	}
}

func (m model) saveModelRoleConfiguration(value config.Config, target, status string) (tea.Model, tea.Cmd) {
	if err := value.Validate(); err != nil {
		m.status = err.Error()
		return m, nil
	}
	if value.HasNativeRole(target) && len(m.models) > 0 {
		if _, err := subagent.Resolve(value, target, m.models); err != nil {
			m.status = err.Error()
			return m, nil
		}
	}
	m.status = "Saving role settings…"
	m.modelFilter.Blur()
	m.modelRoleNameInput.Blur()
	return m, func() tea.Msg {
		if err := m.store.Save(value); err != nil {
			return modelRoleConfiguredMsg{err: err}
		}
		return modelRoleConfiguredMsg{config: value, target: target, status: status}
	}
}
