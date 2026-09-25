package app

import (
	tea "charm.land/bubbletea/v2"
	"fmt"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"maps"
	"strings"
)

func (m model) updateSetup(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.discovering {
		return m, nil
	}
	switch key.String() {
	case "esc":
		if m.runtime != nil {
			if len(m.gatewayConfig.Providers) > 0 {
				m.enterProviderList()
				return m, nil
			}
			return m, tea.Quit
		}
		if m.setupEdit && m.client != nil {
			m.screen = screenChat
			m.status = ""
			return m, m.input.Focus()
		}
		return m, tea.Quit
	case "tab", "down", "enter":
		if key.String() == "enter" && m.setupFocus == setupFieldCount-1 {
			if m.runtime != nil {
				return m.applyProviderEdit()
			}
			return m.discoverModels()
		}
		return m.moveSetupFocus(1)
	case "shift+tab", "up":
		return m.moveSetupFocus(-1)
	}
	if m.runtime != nil && m.setupFocus == setupProviderType {
		switch key.String() {
		case "left":
			m.cycleProviderType(-1)
		case "right", " ":
			m.cycleProviderType(1)
		}
		return m, nil
	}
	if m.runtime != nil && m.setupFocus == setupProviderKind {
		switch key.String() {
		case "left":
			m.cycleProviderKind(-1)
		case "right", " ":
			m.cycleProviderKind(1)
		}
		return m, nil
	}
	var command tea.Cmd
	m.setup[m.setupFocus], command = m.setup[m.setupFocus].Update(key)
	return m, command
}

func (m model) updateProviders(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	providers := m.gatewayConfig.Providers
	if m.discovering {
		return m, nil
	}
	switch key.String() {
	case "esc":
		if m.providerReturn == screenGateway {
			m.enterGatewaySettings()
			return m, nil
		}
		if m.gatewayConfigOnly {
			return m, tea.Quit
		}
		if m.client != nil && m.config.Provider.Model != "model-discovery" {
			m.screen = screenChat
			m.status = ""
			return m, m.input.Focus()
		}
		return m, tea.Quit
	case "up":
		if m.providerCursor > 0 {
			m.providerCursor--
		}
		return m, nil
	case "down":
		if m.providerCursor < len(providers)-1 {
			m.providerCursor++
		}
		return m, nil
	case "a":
		m.enterProviderEditor(-1)
		return m, m.setup[m.setupFocus].Focus()
	case "enter":
		if len(providers) == 0 {
			m.enterProviderEditor(-1)
		} else {
			m.enterProviderEditor(m.providerCursor)
		}
		return m, m.setup[m.setupFocus].Focus()
	case " ":
		if len(providers) == 0 {
			return m, nil
		}
		candidate := cloneGatewayConfig(m.gatewayConfig)
		candidate.Providers[m.providerCursor].Enabled = !candidate.Providers[m.providerCursor].Enabled
		if enabledProviderCount(candidate) == 0 {
			m.status = "At least one provider must remain enabled"
			return m, nil
		}
		return m.applyGatewayConfig(candidate)
	case "d":
		if len(providers) <= 1 {
			m.status = "Add another provider before deleting this one"
			return m, nil
		}
		candidate := cloneGatewayConfig(m.gatewayConfig)
		candidate.Providers = append(candidate.Providers[:m.providerCursor], candidate.Providers[m.providerCursor+1:]...)
		if enabledProviderCount(candidate) == 0 {
			m.status = "At least one provider must remain enabled"
			return m, nil
		}
		if m.providerCursor >= len(candidate.Providers) {
			m.providerCursor = len(candidate.Providers) - 1
		}
		return m.applyGatewayConfig(candidate)
	}
	return m, nil
}

func (m model) applyProviderEdit() (tea.Model, tea.Cmd) {
	id := strings.TrimSpace(m.setup[setupProviderID].Value())
	prefix := strings.TrimSpace(m.setup[setupProviderPrefix].Value())
	providerType := m.selectedProviderType().value
	providerKind := m.selectedProviderKind().value
	if id == "" {
		m.status = "Provider ID is required"
		return m, nil
	}
	if strings.Contains(id, "/") {
		m.status = "Provider ID must not contain '/'"
		return m, nil
	}
	if strings.Contains(prefix, "/") {
		m.status = "Provider prefix must not contain '/'"
		return m, nil
	}
	if providerType == "" {
		m.status = "Provider type is required"
		return m, nil
	}

	candidate := cloneGatewayConfig(m.gatewayConfig)
	editing := !m.providerAdding && m.providerEditIndex >= 0 && m.providerEditIndex < len(candidate.Providers)
	effectivePrefix := prefix
	if effectivePrefix == "" {
		effectivePrefix = id
	}
	for index, existing := range candidate.Providers {
		if editing && index == m.providerEditIndex {
			continue
		}
		if existing.ID == id {
			m.status = "Provider ID " + id + " is already in use"
			return m, nil
		}
		existingPrefix := existing.Prefix
		if existingPrefix == "" {
			existingPrefix = existing.ID
		}
		if existingPrefix == effectivePrefix {
			m.status = "Provider prefix " + effectivePrefix + " is already in use"
			return m, nil
		}
	}
	provider := gateway.ProviderConfig{Enabled: true}
	if editing {
		provider = candidate.Providers[m.providerEditIndex]
	}
	provider.ID = id
	provider.Prefix = prefix
	provider.Type = providerType
	provider.Kind = providerKind
	provider.BaseURL = strings.TrimSpace(m.setup[setupBaseURL].Value())
	provider.APIKeyEnv = strings.TrimSpace(m.setup[setupAPIKeyEnv].Value())
	provider.APIKey = m.setup[setupAPIKey].Value()
	if editing {
		candidate.Providers[m.providerEditIndex] = provider
	} else {
		candidate.Providers = append(candidate.Providers, provider)
	}
	return m.applyGatewayConfig(candidate)
}

func (m model) applyGatewayConfig(candidate gateway.Config) (tea.Model, tea.Cmd) {
	if m.runtime == nil {
		m.status = "internal Gateway runtime is unavailable"
		return m, nil
	}
	if m.gatewayConfigOnly {
		m.discovering = true
		m.status = "Saving Gateway settings…"
		for index := range m.setup {
			m.setup[index].Blur()
		}
		runtime := m.runtime
		return m, func() tea.Msg {
			if err := runtime.Apply(m.ctx, candidate); err != nil {
				return providersAppliedMsg{err: err}
			}
			return providersAppliedMsg{gatewayConfig: candidate}
		}
	}
	m.discovering = true
	returnTarget := ""
	if m.screen == screenModels && m.modelPickerStage == modelPickerContextWindow {
		returnTarget = m.modelTarget
	} else {
		m.modelChooseTarget = false
	}
	m.status = "Saving Gateway settings…"
	for index := range m.setup {
		m.setup[index].Blur()
	}
	m.input.Blur()
	if m.client != nil && m.config.Provider.Model != "model-discovery" {
		m.modelReturn = screenChat
	} else {
		m.modelReturn = screenSetup
	}
	value := m.config
	needsModelDiscovery := strings.TrimSpace(value.Provider.Model) == "" || value.Provider.Model == "model-discovery"
	if strings.TrimSpace(value.Provider.Model) == "" {
		value = config.Default()
		value.Provider.Model = "model-discovery"
	}
	value.UseManagedGateway()
	if returnTarget != "" && m.modelSelection.ID == value.Provider.Model {
		if providerIndex, upstreamID, found := gatewayModelLocation(candidate, m.modelSelection.ID); found {
			value.Provider.ContextWindow = candidate.Providers[providerIndex].ModelMetadata[upstreamID].ContextLength
		}
	}
	runtime := m.runtime
	factory := m.factory
	return m, func() tea.Msg {
		if err := runtime.Apply(m.ctx, candidate); err != nil {
			return providersAppliedMsg{err: err}
		}
		result := providersAppliedMsg{
			config: value, gatewayConfig: candidate, modelTarget: returnTarget, replaceClient: true,
		}
		if returnTarget != "" {
			if err := m.store.Save(value); err != nil {
				result.warning = "save active context cache: " + err.Error()
			}
		}
		configuredClient, err := factory(value)
		if err != nil {
			result.warning = appendStatusWarning(result.warning, "refresh Gateway client: "+err.Error())
			return result
		}
		result.client = configuredClient
		if !needsModelDiscovery {
			return result
		}
		models, err := configuredClient.ListModels(m.ctx)
		if err != nil {
			result.warning = appendStatusWarning(result.warning, "model discovery unavailable: "+err.Error())
			return result
		}
		if len(models) == 0 {
			result.warning = appendStatusWarning(result.warning, "Gateway returned no models")
			return result
		}
		if refreshed, found := refreshModelContextWindow(value, models); found {
			value = refreshed
			if err := m.store.Save(value); err != nil {
				result.warning = appendStatusWarning(result.warning, "save model context: "+err.Error())
			}
		}
		result.models = models
		result.config = value
		result.openModels = true
		return result
	}
}

func appendStatusWarning(existing, next string) string {
	if existing == "" {
		return next
	}
	return existing + " · " + next
}

func enabledProviderCount(value gateway.Config) int {
	count := 0
	for _, provider := range value.Providers {
		if provider.Enabled {
			count++
		}
	}
	return count
}

func cloneGatewayConfig(value gateway.Config) gateway.Config {
	result := value
	result.Providers = append([]gateway.ProviderConfig(nil), value.Providers...)
	for index := range result.Providers {
		source := value.Providers[index].ModelMetadata
		if source == nil {
			continue
		}
		result.Providers[index].ModelMetadata = make(map[string]client.ModelMetadata, len(source))
		maps.Copy(result.Providers[index].ModelMetadata, source)
	}
	return result
}

func containsModel(models []client.Model, id string) bool {
	for _, model := range models {
		if model.ID == id {
			return true
		}
	}
	return false
}
func refreshModelContextWindow(value config.Config, models []client.Model) (config.Config, bool) {
	for _, candidate := range models {
		if candidate.ID == value.Provider.Model {
			value.Provider.ContextWindow = candidate.ContextLength
			return value, true
		}
	}
	return value, false
}

func gatewayModelLocation(value gateway.Config, modelID string) (int, string, bool) {
	prefix, upstreamID, found := strings.Cut(modelID, "/")
	if !found || prefix == "" || upstreamID == "" {
		return 0, "", false
	}
	for index, provider := range value.Providers {
		effectivePrefix := provider.Prefix
		if effectivePrefix == "" {
			effectivePrefix = provider.ID
		}
		if effectivePrefix == prefix {
			return index, upstreamID, true
		}
	}
	return 0, "", false
}

// streamsActiveChat limits token streaming to providers that use the shared
// OpenAI-compatible HTTP transport or the native Codex stream. Native
// Anthropic retains its existing one-shot chat behavior.
func (m model) streamsActiveChat() bool {
	return modelSupportsChatStreaming(m.gatewayConfig, m.activeConfig().ModelGroups, m.activeModel(), nil)
}

func modelSupportsChatStreaming(
	gatewayConfig gateway.Config,
	groups map[string]config.ModelGroupConfig,
	modelID string,
	seen map[string]bool,
) bool {
	if name, grouped := strings.CutPrefix(modelID, "group/"); grouped {
		if seen == nil {
			seen = make(map[string]bool)
		}
		if seen[name] {
			return false
		}
		seen[name] = true
		defer delete(seen, name)
		group, found := groups[name]
		if !found || len(group.Candidates) == 0 {
			return false
		}
		for _, candidate := range group.Candidates {
			if !modelSupportsChatStreaming(gatewayConfig, groups, candidate.Model, seen) {
				return false
			}
		}
		return true
	}
	providerIndex, _, found := gatewayModelLocation(gatewayConfig, modelID)
	if !found {
		return false
	}
	switch gatewayConfig.Providers[providerIndex].Type {
	case "openai-compatible", "openrouter", "xai", "grok", "codex", "codex-app-server":
		return true
	default:
		return false
	}
}

// modelNeedsSystemInstructionCoalescing identifies OpenAI-compatible routes
// whose downstream chat templates may treat developer messages as additional
// system messages and reject them after the first message.
func modelNeedsSystemInstructionCoalescing(
	gatewayConfig gateway.Config,
	groups map[string]config.ModelGroupConfig,
	modelID string,
	seen map[string]bool,
) bool {
	if name, grouped := strings.CutPrefix(modelID, "group/"); grouped {
		if seen == nil {
			seen = make(map[string]bool)
		}
		if seen[name] {
			return false
		}
		seen[name] = true
		defer delete(seen, name)
		group, found := groups[name]
		if !found {
			return false
		}
		for _, candidate := range group.Candidates {
			if modelNeedsSystemInstructionCoalescing(gatewayConfig, groups, candidate.Model, seen) {
				return true
			}
		}
		return false
	}
	providerIndex, _, found := gatewayModelLocation(gatewayConfig, modelID)
	return found && gatewayConfig.Providers[providerIndex].Type == "openai-compatible"
}

func (m model) gatewayContextWindowOverride(modelID string) (int64, bool) {
	providerIndex, upstreamID, found := gatewayModelLocation(m.gatewayConfig, modelID)
	if !found {
		return 0, false
	}
	metadata, configured := m.gatewayConfig.Providers[providerIndex].ModelMetadata[upstreamID]
	return metadata.ContextLength, configured && metadata.ContextLength > 0
}

func (m model) selectedProviderType() providerTypeOption {
	if m.providerTypeCursor < 0 || m.providerTypeCursor >= len(providerTypeOptions) {
		return providerTypeOptions[0]
	}
	return providerTypeOptions[m.providerTypeCursor]
}

func providerKindOptions(providerType string) []providerKindOption {
	automatic := providerKindOption{
		label:       "Auto (inferred)",
		description: "Infer OpenAI semantics, except api.x.ai endpoints are inferred as Grok.",
	}
	if providerType != "openai-compatible" {
		kind := nativeProviderKind(providerType)
		automatic.description = "Infer " + providerKindLabel(kind) + " semantics from this native API type."
		return []providerKindOption{
			automatic,
			{value: kind, label: providerKindLabel(kind), description: "Explicit matching kind for this native API type."},
		}
	}
	return []providerKindOption{
		automatic,
		{value: "generic", label: "Generic", description: "Strict compatible API; disable provider-specific prompt-cache extensions."},
		{value: "openai", label: "OpenAI", description: "Use OpenAI prompt_cache_key semantics."},
		{value: "openrouter", label: "OpenRouter", description: "Use OpenRouter session_id sticky routing semantics."},
		{value: "grok", label: "Grok / xAI", description: "Use Grok conversation-affinity headers and xAI capabilities."},
		{value: "anthropic", label: "Claude / Anthropic", description: "Use Anthropic automatic prompt-cache semantics."},
		{value: "codex", label: "Codex App Server", description: "Use Codex provider-managed conversation semantics."},
	}
}

func nativeProviderKind(providerType string) string {
	switch providerType {
	case "openrouter":
		return "openrouter"
	case "xai", "grok":
		return "grok"
	case "anthropic", "claude":
		return "anthropic"
	case "codex", "codex-app-server":
		return "codex"
	default:
		return "openai"
	}
}

func providerKindLabel(kind string) string {
	switch kind {
	case "openrouter":
		return "OpenRouter"
	case "grok":
		return "Grok / xAI"
	case "anthropic":
		return "Claude / Anthropic"
	case "codex":
		return "Codex App Server"
	default:
		return "OpenAI"
	}
}

func canonicalProviderKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "":
		return ""
	case "openai", "openai-compatible":
		return "openai"
	case "openrouter":
		return "openrouter"
	case "grok", "xai":
		return "grok"
	case "anthropic", "claude":
		return "anthropic"
	case "codex", "codex-app-server":
		return "codex"
	case "generic":
		return "generic"
	default:
		return ""
	}
}

func (m model) selectedProviderKind() providerKindOption {
	options := providerKindOptions(m.selectedProviderType().value)
	if m.providerKindCursor < 0 || m.providerKindCursor >= len(options) {
		return options[0]
	}
	return options[m.providerKindCursor]
}

func (m *model) setProviderKind(value string) {
	value = canonicalProviderKind(value)
	options := providerKindOptions(m.selectedProviderType().value)
	for index, option := range options {
		if option.value == value {
			m.providerKindCursor = index
			return
		}
	}
	// Changing to a native transport must not retain an incompatible semantic
	// kind from the previous OpenAI-compatible route. Empty means infer the
	// matching native kind and always passes the type/kind validation matrix.
	m.providerKindCursor = 0
}

func (m *model) cycleProviderKind(delta int) {
	options := providerKindOptions(m.selectedProviderType().value)
	m.providerKindCursor = (m.providerKindCursor + delta + len(options)) % len(options)
}

func (m *model) setProviderType(value string) {
	switch value {
	case "grok":
		value = "xai"
	case "claude":
		value = "anthropic"
	case "codex-app-server":
		value = "codex"
	}
	for index, option := range providerTypeOptions {
		if option.value == value {
			m.providerTypeCursor = index
			m.updateProviderTypePlaceholders()
			return
		}
	}
	m.providerTypeCursor = 0
	m.updateProviderTypePlaceholders()
}

func (m *model) cycleProviderType(delta int) {
	previous := m.selectedProviderType()
	previousKind := m.selectedProviderKind().value
	baseURLWasDefault := m.setup[setupBaseURL].Value() == previous.baseURL
	apiKeyEnvWasDefault := m.setup[setupAPIKeyEnv].Value() == previous.apiKeyEnv
	m.providerTypeCursor = (m.providerTypeCursor + delta + len(providerTypeOptions)) % len(providerTypeOptions)
	next := m.selectedProviderType()
	m.updateProviderTypePlaceholders()
	m.setProviderKind(previousKind)
	if baseURLWasDefault {
		m.setup[setupBaseURL].SetValue(next.baseURL)
	}
	if apiKeyEnvWasDefault {
		m.setup[setupAPIKeyEnv].SetValue(next.apiKeyEnv)
	}
}

func (m *model) updateProviderTypePlaceholders() {
	selected := m.selectedProviderType()
	m.setup[setupBaseURL].Placeholder = selected.baseURLHint
	m.setup[setupAPIKeyEnv].Placeholder = selected.apiKeyHint
}

func (m model) moveSetupFocus(delta int) (tea.Model, tea.Cmd) {
	m.setup[m.setupFocus].Blur()
	m.setupFocus = (m.setupFocus + delta + setupFieldCount) % setupFieldCount
	if m.runtime != nil && (m.setupFocus == setupProviderType || m.setupFocus == setupProviderKind) {
		return m, nil
	}
	return m, m.setup[m.setupFocus].Focus()
}

func (m model) discoverModels() (tea.Model, tea.Cmd) {
	value := config.Default()
	if m.setupEdit {
		value = m.config
	}
	value.Provider.BaseURL = strings.TrimSpace(m.setup[setupBaseURL].Value())
	// Config validation requires a model, but discovery intentionally happens
	// before the user chooses one.
	value.Provider.Model = "model-discovery"
	value.Provider.APIKeyEnv = strings.TrimSpace(m.setup[setupAPIKeyEnv].Value())
	value.Provider.APIKey = m.setup[setupAPIKey].Value()
	if err := value.Validate(); err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.status = "Loading models…"
	m.discovering = true
	m.modelChooseTarget = false
	m.modelReturn = screenSetup
	for index := range m.setup {
		m.setup[index].Blur()
	}
	return m, func() tea.Msg {
		discoveryClient, err := m.factory(value)
		if err != nil {
			return modelsResultMsg{err: err}
		}
		defer discoveryClient.Close()
		models, err := discoveryClient.ListModels(m.ctx)
		if err != nil {
			return modelsResultMsg{err: fmt.Errorf("load models: %w", err)}
		}
		return modelsResultMsg{models: models, config: value}
	}
}

func (m model) discoverCurrentModels() (tea.Model, tea.Cmd) {
	if m.client == nil {
		m.status = "provider is not configured"
		return m, nil
	}
	m.screen = screenModels
	m.modelReturn = screenChat
	m.modelChooseTarget = true
	m.draftConfig = m.config
	m.models = nil
	m.modelCursor = 0
	m.modelFilter.Reset()
	m.modelFilter.Blur()
	m.input.Blur()
	m.discovering = true
	m.status = "Loading models…"
	configuredClient := m.client
	return m, func() tea.Msg {
		models, err := configuredClient.ListModels(m.ctx)
		if err != nil {
			return modelsResultMsg{err: fmt.Errorf("load models: %w", err)}
		}
		return modelsResultMsg{models: models, config: m.config}
	}
}
