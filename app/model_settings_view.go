package app

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

func (m model) viewSetup() string {
	labels := []string{"Provider ID", "Model prefix", "API type", "Provider kind (optional)", "Base URL", "API key environment variable", "API key (optional, stored in config)"}
	var body strings.Builder
	title := "q · first-run setup"
	if m.setupEdit {
		title = "q · provider settings"
	}
	if m.runtime != nil {
		title = "q · add provider"
		if m.setupEdit {
			title = "q · edit provider"
		}
	}
	body.WriteString(titleStyle.Render(title))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	if m.runtime != nil {
		body.WriteString(subtleStyle.Render("Managed by q's internal llm-provider Gateway"))
	} else {
		body.WriteString(subtleStyle.Render("OpenAI-compatible provider · " + m.store.Path()))
	}
	body.WriteString("\n\n")
	for index, field := range m.setup {
		label := subtleStyle.Render(labels[index])
		if index == m.setupFocus {
			label = activeLabelStyle.Render(labels[index])
		}
		body.WriteString(label)
		body.WriteString("\n")
		if m.runtime != nil && index == setupProviderType {
			selected := m.selectedProviderType()
			selector := "  " + selected.label
			if index == m.setupFocus {
				selector = "‹ " + selected.label + " ›"
			}
			body.WriteString(activeLabelStyle.Render(selector))
			body.WriteString("\n")
			body.WriteString(subtleStyle.Render(selected.description))
		} else if m.runtime != nil && index == setupProviderKind {
			selected := m.selectedProviderKind()
			selector := "  " + selected.label
			if index == m.setupFocus {
				selector = "‹ " + selected.label + " ›"
			}
			body.WriteString(activeLabelStyle.Render(selector))
			body.WriteString("\n")
			body.WriteString(subtleStyle.Render(selected.description))
			body.WriteString("\n")
			if m.selectedProviderType().value == "openai-compatible" {
				body.WriteString(subtleStyle.Render("Compatible APIs accept any listed kind; use Generic for strict or unknown servers."))
			} else {
				body.WriteString(subtleStyle.Render("Native APIs accept only their matching kind; Auto is recommended."))
			}
		} else {
			body.WriteString(field.View())
		}
		body.WriteString("\n\n")
	}
	if m.status != "" {
		statusStyle := errorStyle
		if m.discovering {
			statusStyle = subtleStyle
		}
		body.WriteString(statusStyle.Render(m.status))
		body.WriteString("\n")
	}
	help := "tab/↑/↓ navigate · enter next/apply · esc quit"
	if m.setupEdit {
		help = "tab/↑/↓ navigate · enter next/apply · esc cancel"
	}
	if m.runtime != nil && m.setupFocus == setupProviderType {
		help = "←/→ select API type · tab/↑/↓ navigate · enter next · esc cancel"
	} else if m.runtime != nil && m.setupFocus == setupProviderKind {
		help = "←/→ select provider kind · tab/↑/↓ navigate · enter next · esc cancel"
	}
	body.WriteString(helpStyle.Render(help))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewProviders() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · providers"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render("Internal llm-provider Gateway · " + m.runtime.Endpoint()))
	body.WriteString("\n\n")
	if len(m.gatewayConfig.Providers) == 0 {
		body.WriteString(emptyStyle.Render("No providers configured"))
		body.WriteString("\n")
	} else {
		for index, provider := range m.gatewayConfig.Providers {
			prefix := "  "
			style := subtleStyle
			if index == m.providerCursor {
				prefix = "› "
				style = activeLabelStyle
			}
			state := "disabled"
			if provider.Enabled {
				state = "enabled"
			}
			modelPrefix := provider.Prefix
			if modelPrefix == "" {
				modelPrefix = provider.ID
			}
			body.WriteString(prefix)
			body.WriteString(style.Render(provider.ID))
			detail := "  " + provider.Type
			if provider.Kind != "" {
				detail += " · kind " + provider.Kind
			}
			body.WriteString(subtleStyle.Render(detail + " · prefix " + modelPrefix + " · " + state))
			body.WriteString("\n")
		}
	}
	if m.status != "" {
		body.WriteString("\n")
		style := errorStyle
		if m.discovering {
			style = subtleStyle
		}
		body.WriteString(style.Render(m.status))
		body.WriteString("\n")
	}
	body.WriteString("\n")
	help := "↑/↓ select · enter edit · a add · space enable/disable · d delete · esc chat"
	if m.providerReturn == screenGateway {
		help = "↑/↓ select · enter edit · a add · space enable/disable · d delete · esc Gateway"
	} else if m.gatewayConfigOnly {
		help = "↑/↓ select · enter edit · a add · space enable/disable · d delete · esc quit"
	}
	body.WriteString(helpStyle.Render(help))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewModels() string {
	switch m.modelPickerStage {
	case modelPickerTargets:
		return m.viewModelTargets()
	case modelPickerGroups:
		return m.viewModelGroups()
	case modelPickerGroupName:
		return m.viewModelGroupName()
	case modelPickerGroupCandidates:
		return m.viewModelGroupCandidates()
	case modelPickerGroupCandidateModels:
		return m.viewModelGroupCandidateModels()
	case modelPickerGroupCandidateReasoning:
		return m.viewModelGroupCandidateReasoning()
	case modelPickerGroupCandidateTimeout:
		return m.viewModelGroupCandidateTimeout()
	case modelPickerReasoning:
		return m.viewReasoningEfforts()
	case modelPickerEmbeddingDimensions:
		return m.viewEmbeddingDimensions()
	case modelPickerContextWindow:
		return m.viewContextWindow()
	}
	filtered := m.filteredModels()
	visible := max(4, m.height-10)
	start := 0
	if m.modelCursor >= visible {
		start = m.modelCursor - visible + 1
	}
	end := min(len(filtered), start+visible)

	var body strings.Builder
	title := m.modelPickerTitle()
	body.WriteString(titleStyle.Render(title))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render(m.modelTargetScope()))
	body.WriteString("\n")
	endpoint := m.draftConfig.Provider.BaseURL
	if m.runtime != nil {
		endpoint = m.runtime.Endpoint()
	}
	body.WriteString(subtleStyle.Render(endpoint))
	body.WriteString("\n\n")
	body.WriteString(m.modelFilter.View())
	body.WriteString("\n\n")
	if m.discovering {
		body.WriteString(subtleStyle.Render("Loading models…"))
	} else if len(filtered) == 0 {
		body.WriteString(emptyStyle.Render("No matching models"))
	} else {
		for index := start; index < end; index++ {
			prefix := "  "
			style := subtleStyle
			if index == m.modelCursor {
				prefix = "› "
				style = activeLabelStyle
			}
			body.WriteString(prefix)
			body.WriteString(style.Render(filtered[index].ID))
			if group, grouped := modelGroupChoice(m.draftConfig, filtered[index].ID); grouped {
				body.WriteString(subtleStyle.Render(fmt.Sprintf("  · %d ordered candidates", len(m.draftConfig.ModelGroups[group].Candidates))))
			} else if filtered[index].ContextLength > 0 {
				body.WriteString(subtleStyle.Render("  · context " + formatTokenCount(filtered[index].ContextLength)))
			} else {
				body.WriteString(subtleStyle.Render("  · context unknown"))
			}
			if _, grouped := modelGroupChoice(m.draftConfig, filtered[index].ID); !grouped {
				if _, overridden := m.gatewayContextWindowOverride(filtered[index].ID); overridden {
					body.WriteString(subtleStyle.Render(" · Gateway override"))
				}
			}
			body.WriteString("\n")
		}
	}
	body.WriteString("\n")
	if m.discovering {
		body.WriteString(helpStyle.Render("loading models · ctrl+c quit"))
	} else {
		available := m.selectableModels()
		help := fmt.Sprintf("%d/%d choices · type to filter · ↑/↓ select · ctrl+e GLOBAL context · enter %s · esc back", len(filtered), len(available), m.modelSaveLabel())
		body.WriteString(helpStyle.Render(help))
	}
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewModelTargets() string {
	targets := m.modelTargets()
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · model assignments"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render("Select a Role × Scope cell. GLOBAL writes ~/.q/config.yaml; WORKSPACE writes .q/model.json."))
	body.WriteString("\n\n")
	if m.discovering {
		body.WriteString(subtleStyle.Render("Loading models…"))
	} else {
		roleWidth := 12
		cellWidth := max(18, (max(52, m.width-10)-roleWidth)/2)
		body.WriteString(subtleStyle.Render(padModelCell("ROLE", roleWidth)))
		globalHeader := padModelCell("GLOBAL", cellWidth)
		workspaceHeader := padModelCell("WORKSPACE", cellWidth)
		if m.modelScopeCursor == modelScopeGlobal {
			body.WriteString(activeLabelStyle.Render(globalHeader))
			body.WriteString(subtleStyle.Render(workspaceHeader))
		} else {
			body.WriteString(subtleStyle.Render(globalHeader))
			body.WriteString(activeLabelStyle.Render(workspaceHeader))
		}
		body.WriteString("\n")
		for index, target := range targets {
			body.WriteString(subtleStyle.Render(padModelCell(target, roleWidth)))
			body.WriteString(m.renderModelTableCell(
				m.globalModelSummary(target), cellWidth,
				index == m.modelTargetCursor && m.modelScopeCursor == modelScopeGlobal, false,
			))
			body.WriteString(m.renderModelTableCell(
				m.workspaceModelSummary(target), cellWidth,
				index == m.modelTargetCursor && m.modelScopeCursor == modelScopeWorkspace,
				!m.workspaceModelEditable(target),
			))
			body.WriteString("\n")
		}
	}
	if m.status != "" && !m.discovering {
		body.WriteString("\n")
		body.WriteString(subtleStyle.Render(m.status))
		body.WriteString("\n")
	}
	body.WriteString("\n")
	help := "←/→ scope · ↑/↓ role · enter change · i reset selected cell · g global groups · esc chat"
	if m.isStandaloneScreen(screenModels) {
		help = "←/→ scope · ↑/↓ role · enter change · i reset selected cell · g global groups · esc quit"
	}
	body.WriteString(helpStyle.Render(help))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) modelPickerTitle() string {
	scope := "GLOBAL"
	if m.modelWorkspace {
		scope = "WORKSPACE"
	}
	return "q · " + scope + " · " + m.modelTarget + " model"
}

func (m model) modelTargetScope() string {
	if m.modelWorkspace {
		return "Scope · WORKSPACE · Enter saves this Role override to .q/model.json"
	}
	return "Scope · GLOBAL · saves to ~/.q/config.yaml"
}

func (m model) modelSaveLabel() string {
	if m.modelWorkspace {
		return "save workspace override"
	}
	switch m.modelTarget {
	case "", defaultModelTarget:
		return "continue/save global default"
	case embeddingModelTarget:
		return "continue global embedding setting"
	default:
		return "continue/save global role setting"
	}
}

func (m model) renderModelTableCell(value string, width int, selected, disabled bool) string {
	prefix := "  "
	style := subtleStyle
	if disabled {
		value = "— " + value
	}
	if selected {
		prefix = "› "
		style = activeLabelStyle
	}
	return style.Render(padModelCell(prefix+value, width))
}

func padModelCell(value string, width int) string {
	value = ansi.Truncate(value, max(1, width-1), "…")
	return value + strings.Repeat(" ", max(0, width-ansi.StringWidth(value)))
}

func (m model) viewModelGroups() string {
	names := modelGroupNames(m.draftConfig)
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · model groups"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render("Scope · GLOBAL · saves to ~/.q/config.yaml"))
	body.WriteString("\n")
	body.WriteString(subtleStyle.Render("Ordered Gateway models with transient fallback"))
	body.WriteString("\n\n")
	if len(names) == 0 {
		body.WriteString(emptyStyle.Render("No model groups. Press a to create one."))
		body.WriteString("\n")
	} else {
		for index, name := range names {
			prefix := "  "
			style := subtleStyle
			if index == m.modelGroupCursor {
				prefix = "› "
				style = activeLabelStyle
			}
			group := m.draftConfig.ModelGroups[name]
			body.WriteString(prefix)
			body.WriteString(style.Render(name))
			body.WriteString(subtleStyle.Render(fmt.Sprintf("  · %d candidates", len(group.Candidates))))
			if contextLength, _ := modelGroupLimits(group, m.models); contextLength > 0 {
				body.WriteString(subtleStyle.Render(" · context " + formatTokenCount(contextLength)))
			}
			body.WriteString("\n")
			if len(group.Candidates) > 0 {
				models := make([]string, len(group.Candidates))
				for candidateIndex, candidate := range group.Candidates {
					models[candidateIndex] = candidate.Model
				}
				body.WriteString(subtleStyle.Render("    " + strings.Join(models, " → ")))
				body.WriteString("\n")
			}
		}
	}
	if m.status != "" {
		body.WriteString("\n")
		body.WriteString(subtleStyle.Render(m.status))
		body.WriteString("\n")
	}
	body.WriteString("\n")
	body.WriteString(helpStyle.Render("a add · enter edit · d delete · esc model settings"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewModelGroupName() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · new model group"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render("The Gateway model ID will be group/<name>."))
	body.WriteString("\n\n")
	body.WriteString(m.modelGroupNameInput.View())
	if m.status != "" {
		body.WriteString("\n\n")
		body.WriteString(errorStyle.Render(m.status))
	}
	body.WriteString("\n\n")
	body.WriteString(helpStyle.Render("enter continue · esc groups"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewModelGroupCandidates() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · model group · " + m.modelGroupName))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	contextLength, output := modelGroupLimits(m.modelGroupDraft, m.models)
	limits := "context unknown"
	if contextLength > 0 {
		limits = "context " + formatTokenCount(contextLength)
	}
	if output > 0 {
		limits += " · output " + formatTokenCount(output)
	}
	body.WriteString(subtleStyle.Render(limits))
	body.WriteString("\n\n")
	if len(m.modelGroupDraft.Candidates) == 0 {
		body.WriteString(emptyStyle.Render("No candidates. Press a to add one."))
		body.WriteString("\n")
	} else {
		for index, candidate := range m.modelGroupDraft.Candidates {
			prefix := "  "
			style := subtleStyle
			if index == m.modelGroupCandidateCursor {
				prefix = "› "
				style = activeLabelStyle
			}
			body.WriteString(prefix)
			body.WriteString(style.Render(fmt.Sprintf("%d. %s", index+1, candidate.Model)))
			detail := "provider default effort"
			if candidate.ReasoningEffort != "" {
				detail = "effort " + candidate.ReasoningEffort
			}
			if candidate.Timeout > 0 {
				detail += " · timeout " + candidate.Timeout.String()
			} else {
				detail += " · no timeout"
			}
			body.WriteString(subtleStyle.Render("  · " + detail))
			body.WriteString("\n")
		}
	}
	if m.status != "" {
		body.WriteString("\n")
		body.WriteString(subtleStyle.Render(m.status))
		body.WriteString("\n")
	}
	body.WriteString("\n")
	body.WriteString(helpStyle.Render("a add · enter edit · ctrl+↑/↓ reorder · d remove · esc groups (auto-save)"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewModelGroupCandidateModels() string {
	filtered := m.filteredCandidateModels()
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · " + m.modelGroupName + " · candidate model"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(m.modelFilter.View())
	body.WriteString("\n\n")
	if len(filtered) == 0 {
		body.WriteString(emptyStyle.Render("No matching models"))
	} else {
		visible := max(4, m.height-10)
		start := max(0, m.modelCursor-visible+1)
		end := min(len(filtered), start+visible)
		for index := start; index < end; index++ {
			prefix := "  "
			style := subtleStyle
			if index == m.modelCursor {
				prefix = "› "
				style = activeLabelStyle
			}
			body.WriteString(prefix)
			body.WriteString(style.Render(filtered[index].ID))
			if filtered[index].ContextLength > 0 {
				body.WriteString(subtleStyle.Render("  · context " + formatTokenCount(filtered[index].ContextLength)))
			}
			body.WriteString("\n")
		}
	}
	body.WriteString("\n")
	body.WriteString(helpStyle.Render("type to filter · ↑/↓ select · enter next · esc candidates"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewModelGroupCandidateReasoning() string {
	reasoning := selectedReasoning(m.modelSelection)
	efforts := []string{""}
	if reasoning != nil {
		efforts = append(efforts, reasoning.SupportedEfforts...)
	}
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · " + m.modelGroupName + " · candidate effort"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render(m.modelSelection.ID))
	body.WriteString("\n\n")
	for index, effort := range efforts {
		label := effort
		if label == "" {
			label = "provider default"
		}
		prefix := "  "
		style := subtleStyle
		if index == m.reasoningCursor {
			prefix = "› "
			style = activeLabelStyle
		}
		body.WriteString(prefix + style.Render(label) + "\n")
	}
	body.WriteString("\n")
	body.WriteString(helpStyle.Render("↑/↓ select · enter timeout · esc model"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewModelGroupCandidateTimeout() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · " + m.modelGroupName + " · candidate timeout"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render(m.modelGroupCandidate.Model + " · empty means no additional deadline"))
	body.WriteString("\n\n")
	body.WriteString(m.modelGroupTimeoutInput.View())
	if m.status != "" {
		body.WriteString("\n\n")
		body.WriteString(errorStyle.Render(m.status))
	}
	body.WriteString("\n\n")
	body.WriteString(helpStyle.Render("enter apply candidate · esc back"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func modelGroupAvailable(group config.ModelGroupConfig, models []client.Model) bool {
	if len(group.Candidates) == 0 {
		return false
	}
	available := make(map[string]struct{}, len(models))
	for _, model := range models {
		available[model.ID] = struct{}{}
	}
	for _, candidate := range group.Candidates {
		if _, found := available[candidate.Model]; !found {
			return false
		}
	}
	return true
}

func modelGroupLimits(group config.ModelGroupConfig, models []client.Model) (int64, int64) {
	byID := make(map[string]client.Model, len(models))
	for _, model := range models {
		byID[model.ID] = model
	}
	var contextLength, output int64
	contextKnown := len(group.Candidates) > 0
	outputKnown := len(group.Candidates) > 0
	for _, candidate := range group.Candidates {
		model, found := byID[candidate.Model]
		if !found || model.ContextLength <= 0 {
			contextKnown = false
		} else if contextLength == 0 || model.ContextLength < contextLength {
			contextLength = model.ContextLength
		}
		if !found || model.MaxOutputTokens <= 0 {
			outputKnown = false
		} else if output == 0 || model.MaxOutputTokens < output {
			output = model.MaxOutputTokens
		}
	}
	if !contextKnown {
		contextLength = 0
	}
	if !outputKnown {
		output = 0
	}
	return contextLength, output
}

func (m model) viewReasoningEfforts() string {
	reasoning := selectedReasoning(m.modelSelection)
	efforts := []string{""}
	if reasoning != nil {
		efforts = append(efforts, reasoning.SupportedEfforts...)
	}
	var body strings.Builder
	target := m.modelTarget
	if target == "" {
		target = defaultModelTarget
	}
	body.WriteString(titleStyle.Render("q · " + target + " reasoning effort"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render(m.modelSelection.ID))
	body.WriteString("\n\n")
	for index, effort := range efforts {
		label := effort
		if effort == "" {
			label = "default"
		}
		prefix := "  "
		style := subtleStyle
		if index == m.reasoningCursor {
			prefix = "› "
			style = activeLabelStyle
		}
		body.WriteString(prefix)
		body.WriteString(style.Render(label))
		if effort == "" {
			body.WriteString(subtleStyle.Render("  omit reasoning_effort"))
		} else if reasoning != nil && effort == reasoning.DefaultEffort {
			body.WriteString(subtleStyle.Render("  provider default"))
		}
		body.WriteString("\n")
	}
	body.WriteString("\n")
	body.WriteString(helpStyle.Render("↑/↓ select · enter apply · esc models"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewEmbeddingDimensions() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · embedding dimensions"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render(m.modelSelection.ID))
	body.WriteString("\n\n")
	body.WriteString(m.embeddingDimensions.View())
	body.WriteString("\n")
	body.WriteString(subtleStyle.Render("The configured size will be sent with embedding requests and select the workspace HNSW index."))
	if m.status != "" {
		body.WriteString("\n\n")
		body.WriteString(errorStyle.Render(m.status))
	}
	body.WriteString("\n\n")
	body.WriteString(helpStyle.Render("enter apply · esc models"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}
func (m model) viewContextWindow() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · GLOBAL Gateway model context"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render("Scope · GLOBAL provider metadata · applies wherever this Gateway model is used"))
	body.WriteString("\n")
	body.WriteString(subtleStyle.Render(m.modelSelection.ID))
	body.WriteString("\n\n")
	body.WriteString(m.modelContextWindow.View())
	body.WriteString("\n")
	if m.modelSelection.ContextLength > 0 {
		body.WriteString(subtleStyle.Render("Current Gateway value · " + formatTokenCount(m.modelSelection.ContextLength)))
	} else {
		body.WriteString(subtleStyle.Render("Current Gateway value · unknown"))
	}
	body.WriteString("\n")
	body.WriteString(subtleStyle.Render("Set a positive token count. Leave empty to remove the Gateway override."))
	if m.status != "" {
		body.WriteString("\n\n")
		body.WriteString(errorStyle.Render(m.status))
	}
	body.WriteString("\n\n")
	body.WriteString(helpStyle.Render("enter apply and refresh · esc models"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) globalModelSummary(target string) string {
	value := m.draftConfig
	if target == defaultModelTarget {
		if effort := value.Provider.EffectiveReasoningEffort(); effort != "" {
			return value.Provider.Model + " · effort " + effort
		}
		return value.Provider.Model
	}
	if target == embeddingModelTarget {
		if value.Embedding.Model == "" {
			return "not configured"
		}
		return value.Embedding.Model
	}
	agent, configured := value.Agents.Roles[target]
	if configured && agent.Group != "" {
		return "group/" + agent.Group
	}
	if configured && agent.Model != "" {
		return agent.Model
	}
	return "inherit → " + value.Provider.Model
}

func (m model) workspaceModelSummary(target string) string {
	if !workspace.ModelOverrideAllowed(target) {
		return "shared GLOBAL"
	}
	if m.workspaceStore == nil {
		return "unavailable"
	}
	if override, found := m.workspaceOverride(target); found {
		return override.Model
	}
	if target == defaultModelTarget {
		return "inherit → " + m.draftConfig.Provider.Model
	}
	agent, err := m.activeConfig().EffectiveAgent(target)
	if err != nil {
		return "inherit"
	}
	if agent.Group != "" {
		return "inherit → group/" + agent.Group
	}
	return "inherit → " + agent.Model
}

func (m model) workspaceModelEditable(target string) bool {
	return m.workspaceStore != nil && workspace.ModelOverrideAllowed(target)
}

func modelTargetLabel(target string) string {
	if target == defaultModelTarget {
		return "default"
	}
	return target
}
