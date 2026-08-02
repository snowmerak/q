package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/memory"
	"golang.org/x/text/unicode/norm"
)

type screen uint8

const (
	screenSetup screen = iota
	screenProviders
	screenModels
	screenChat
)

const (
	setupProviderID = iota
	setupProviderPrefix
	setupProviderType
	setupBaseURL
	setupAPIKeyEnv
	setupAPIKey
	setupFieldCount
)

type providerTypeOption struct {
	value       string
	label       string
	description string
	baseURL     string
	apiKeyEnv   string
	baseURLHint string
	apiKeyHint  string
}

var providerTypeOptions = []providerTypeOption{
	{
		value: "openai-compatible", label: "OpenAI compatible",
		description: "OpenAI-compatible HTTP API, including local servers",
		baseURL:     "https://api.openai.com/v1", apiKeyEnv: "OPENAI_API_KEY",
		baseURLHint: "https://api.openai.com/v1", apiKeyHint: "OPENAI_API_KEY",
	},
	{
		value: "openrouter", label: "OpenRouter",
		description: "OpenRouter API with native reasoning and cache controls",
		apiKeyEnv:   "OPENROUTER_API_KEY", baseURLHint: "default: https://openrouter.ai/api/v1", apiKeyHint: "OPENROUTER_API_KEY",
	},
	{
		value: "xai", label: "xAI",
		description: "xAI API for Grok models",
		apiKeyEnv:   "XAI_API_KEY", baseURLHint: "default: https://api.x.ai/v1", apiKeyHint: "XAI_API_KEY",
	},
	{
		value: "anthropic", label: "Anthropic",
		description: "Native Anthropic Messages API",
		apiKeyEnv:   "ANTHROPIC_API_KEY", baseURLHint: "optional; uses the Anthropic default", apiKeyHint: "ANTHROPIC_API_KEY",
	},
	{
		value: "codex", label: "Codex App Server",
		description: "Local codex app-server using the current Codex login",
		baseURLHint: "not used", apiKeyHint: "not used",
	},
}

type model struct {
	ctx     context.Context
	store   config.Store
	factory clientFactory
	screen  screen
	width   int
	height  int
	status  string
	runtime providerRuntime

	setup              [setupFieldCount]textinput.Model
	setupFocus         int
	setupEdit          bool
	providerEditIndex  int
	providerAdding     bool
	providerTypeCursor int
	gatewayConfig      gateway.Config
	providerCursor     int
	discovering        bool
	models             []client.Model
	modelCursor        int
	modelFilter        textinput.Model
	draftConfig        config.Config
	modelReturn        screen

	config           config.Config
	client           chatClient
	messages         []client.Message
	memory           *memory.Manager
	conversationID   string
	pendingMessage   client.Message
	requestEstimate  int
	compactionTarget int
	input            textarea.Model
	viewport         viewport.Model
	spinner          spinner.Model
	waiting          bool
	compacting       bool
	submitPending    bool
}

type configuredMsg struct {
	config          config.Config
	client          chatClient
	preserveHistory bool
	err             error
}

type chatResultMsg struct {
	response *client.ChatResponse
	err      error
}

type compactionResultMsg struct {
	response *client.ChatResponse
	plan     memory.Plan
	err      error
}

type deferredSubmitMsg struct{}

const imeCommitGracePeriod = 35 * time.Millisecond

type modelsResultMsg struct {
	models []client.Model
	config config.Config
	err    error
}

type providersAppliedMsg struct {
	models        []client.Model
	config        config.Config
	gatewayConfig gateway.Config
	client        chatClient
	err           error
}

func newModel(ctx context.Context, store config.Store, factory clientFactory) model {
	return newManagedModel(ctx, store, factory, nil)
}

func newManagedModel(ctx context.Context, store config.Store, factory clientFactory, runtime providerRuntime) model {
	defaults := config.Default()
	m := model{
		ctx: ctx, store: store, factory: factory, runtime: runtime, screen: screenSetup,
		viewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(12)),
		spinner:  spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
	if runtime != nil {
		m.gatewayConfig = runtime.Config()
	}
	m.viewport.SoftWrap = true

	placeholders := []string{
		"provider id",
		"optional; defaults to provider id",
		"",
		"https://api.openai.com/v1",
		"OPENAI_API_KEY",
		"optional inline key",
	}
	values := []string{
		"default",
		"",
		"",
		defaults.Provider.BaseURL,
		defaults.Provider.APIKeyEnv,
		defaults.Provider.APIKey,
	}
	for index := range m.setup {
		field := textinput.New()
		field.Prompt = ""
		field.Placeholder = placeholders[index]
		field.SetValue(values[index])
		field.SetWidth(60)
		m.setup[index] = field
	}
	m.setup[setupAPIKey].EchoMode = textinput.EchoPassword
	m.setProviderType("openai-compatible")
	m.setup[m.setupFocus].Focus()
	m.modelFilter = textinput.New()
	m.modelFilter.Prompt = "/ "
	m.modelFilter.Placeholder = "Filter models"
	m.modelFilter.SetWidth(60)
	m.input = newChatInput()
	if runtime != nil && len(m.gatewayConfig.Providers) == 0 {
		m.enterProviderEditor(-1)
	}
	return m
}

func newChatInput() textarea.Model {
	input := textarea.New()
	// A real terminal cursor gives Windows IMEs an accurate composition and
	// candidate-window position. The default virtual cursor is only painted text.
	input.SetVirtualCursor(false)
	styles := input.Styles()
	styles.Cursor.Shape = tea.CursorBar
	input.SetStyles(styles)
	input.Placeholder = "Type a message…"
	input.Prompt = "│ "
	input.ShowLineNumbers = false
	input.SetHeight(4)
	input.SetWidth(80)
	input.CharLimit = 32_000
	input.KeyMap.InsertNewline.SetKeys("shift+enter")
	input.KeyMap.InsertNewline.SetHelp("shift+enter", "insert newline")
	return input
}

func (m model) Init() tea.Cmd {
	if m.screen == screenChat {
		return tea.Batch(m.input.Focus(), tea.RequestBackgroundColor)
	}
	if m.screen == screenModels {
		return tea.Batch(m.modelFilter.Focus(), tea.RequestBackgroundColor)
	}
	if m.screen == screenProviders {
		return tea.RequestBackgroundColor
	}
	return tea.Batch(m.setup[m.setupFocus].Focus(), tea.RequestBackgroundColor)
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	message = normalizeTextInputMessage(message)
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.resize(message.Width, message.Height)
		return m, nil
	case tea.BackgroundColorMsg:
		m.applyColorScheme(message.IsDark())
		return m, nil
	case configuredMsg:
		if message.err != nil {
			m.status = message.err.Error()
			if m.screen == screenModels {
				return m, m.modelFilter.Focus()
			}
			return m, m.setup[m.setupFocus].Focus()
		}
		if m.client != nil && m.client != message.client {
			_ = m.client.Close()
		}
		if message.preserveHistory {
			m.screen = screenChat
			m.setupEdit = false
			m.config = message.config
			m.client = message.client
			m.conversationID = ""
			m.compactionTarget = 0
			if m.memory != nil {
				m.memory.Configure(memoryPolicy(message.config))
			}
			m.status = "Model changed to " + message.config.Provider.Model
			m.refreshTranscript()
		} else {
			m.enterChat(message.config, message.client)
		}
		return m, m.input.Focus()
	case modelsResultMsg:
		m.discovering = false
		if message.err != nil {
			m.status = message.err.Error()
			if m.modelReturn == screenChat {
				m.screen = screenChat
				return m, m.input.Focus()
			}
			m.screen = screenSetup
			return m, m.setup[m.setupFocus].Focus()
		}
		m.enterModelPicker(message.config, message.models)
		if m.screen == screenSetup {
			return m, m.setup[m.setupFocus].Focus()
		}
		if m.screen == screenChat {
			return m, m.input.Focus()
		}
		return m, m.modelFilter.Focus()
	case providersAppliedMsg:
		m.discovering = false
		if message.err != nil {
			m.status = message.err.Error()
			if m.screen == screenProviders {
				return m, nil
			}
			return m, m.setup[m.setupFocus].Focus()
		}
		if m.client != nil && m.client != message.client {
			_ = m.client.Close()
		}
		m.client = message.client
		m.gatewayConfig = message.gatewayConfig
		if strings.TrimSpace(m.config.Provider.Model) == "" {
			m.config = message.config
		}
		m.conversationID = ""
		m.compactionTarget = 0
		m.enterModelPicker(message.config, message.models)
		return m, m.modelFilter.Focus()
	case chatResultMsg:
		m.waiting = false
		m.compacting = false
		m.pendingMessage = client.Message{}
		if message.err != nil {
			m.compactionTarget = 0
			m.status = message.err.Error()
			return m, m.input.Focus()
		}
		if message.response == nil || len(message.response.Choices) == 0 {
			m.compactionTarget = 0
			m.status = "provider returned no choices"
			return m, m.input.Focus()
		}
		assistant := message.response.Choices[0].Message
		if assistant.Role == "" {
			assistant.Role = client.RoleAssistant
		}
		m.messages = append(m.messages, assistant)
		if m.memory != nil {
			m.memory.ObserveUsage(message.response.Usage.PromptTokens, m.requestEstimate)
			m.memory.Append(assistant)
		}
		if message.response.ConversationID != "" {
			m.conversationID = message.response.ConversationID
		}
		if m.compactionTarget > 0 && message.response.Usage.PromptTokens > m.compactionTarget {
			m.status = fmt.Sprintf("Context remains above target · %s/%s", formatTokens(message.response.Usage.PromptTokens), formatTokens(m.compactionTarget))
		} else {
			m.status = ""
		}
		m.compactionTarget = 0
		m.refreshTranscript()
		return m, m.input.Focus()
	case compactionResultMsg:
		if message.err != nil {
			m.rollbackPendingMessage()
			m.status = "compact context: " + message.err.Error()
			return m, m.input.Focus()
		}
		if message.response == nil || len(message.response.Choices) == 0 {
			m.rollbackPendingMessage()
			m.status = "compact context: provider returned no choices"
			return m, m.input.Focus()
		}
		summary := strings.TrimSpace(message.response.Choices[0].Message.TextContent())
		if err := m.memory.Apply(message.plan, summary); err != nil {
			m.rollbackPendingMessage()
			m.status = "compact context: " + err.Error()
			return m, m.input.Focus()
		}
		m.conversationID = ""
		m.compacting = false
		m.compactionTarget = message.plan.TargetTokens
		m.status = fmt.Sprintf("Context compacted · %s → %s", formatTokens(message.plan.BeforeTokens), formatTokens(m.memory.PredictedTokens()))
		return m, m.sendChatRequest()
	case deferredSubmitMsg:
		if !m.submitPending {
			return m, nil
		}
		m.submitPending = false
		return m.submitChat()
	}

	if key, ok := message.(tea.KeyPressMsg); ok {
		if key.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.screen == screenSetup {
			return m.updateSetup(key)
		}
		if m.screen == screenProviders {
			return m.updateProviders(key)
		}
		if m.screen == screenModels {
			return m.updateModelPicker(key)
		}
		return m.updateChatKey(key)
	}

	if m.screen == screenChat {
		var commands []tea.Cmd
		var command tea.Cmd
		m.viewport, command = m.viewport.Update(message)
		commands = append(commands, command)
		if m.waiting {
			m.spinner, command = m.spinner.Update(message)
			commands = append(commands, command)
		} else {
			m.input, command = m.input.Update(message)
			commands = append(commands, command)
		}
		return m, tea.Batch(commands...)
	}
	return m, nil
}

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
	m.discovering = true
	m.status = "Starting and validating Gateway…"
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
	if strings.TrimSpace(value.Provider.Model) == "" {
		value = config.Default()
		value.Provider.Model = "model-discovery"
	}
	value.UseManagedGateway()
	runtime := m.runtime
	factory := m.factory
	return m, func() tea.Msg {
		if err := runtime.Apply(m.ctx, candidate); err != nil {
			return providersAppliedMsg{err: err}
		}
		configuredClient, err := factory(value)
		if err != nil {
			return providersAppliedMsg{err: err}
		}
		models, err := configuredClient.ListModels(m.ctx)
		if err != nil {
			_ = configuredClient.Close()
			return providersAppliedMsg{err: fmt.Errorf("load Gateway models: %w", err)}
		}
		return providersAppliedMsg{
			models: models, config: value, gatewayConfig: candidate, client: configuredClient,
		}
	}
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

func (m model) selectedProviderType() providerTypeOption {
	if m.providerTypeCursor < 0 || m.providerTypeCursor >= len(providerTypeOptions) {
		return providerTypeOptions[0]
	}
	return providerTypeOptions[m.providerTypeCursor]
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
	baseURLWasDefault := m.setup[setupBaseURL].Value() == previous.baseURL
	apiKeyEnvWasDefault := m.setup[setupAPIKeyEnv].Value() == previous.apiKeyEnv
	m.providerTypeCursor = (m.providerTypeCursor + delta + len(providerTypeOptions)) % len(providerTypeOptions)
	next := m.selectedProviderType()
	m.updateProviderTypePlaceholders()
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
	if m.runtime != nil && m.setupFocus == setupProviderType {
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

func (m model) updateModelPicker(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.discovering {
		return m, nil
	}
	filtered := m.filteredModels()
	switch key.String() {
	case "esc":
		if m.runtime != nil && m.modelReturn == screenChat && !containsModel(filtered, m.config.Provider.Model) {
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
	case "enter":
		if len(filtered) == 0 {
			return m, nil
		}
		value := m.draftConfig
		selected := filtered[m.modelCursor]
		value.Provider.Model = selected.ID
		value.Provider.ContextWindow = selected.ContextLength
		return m.saveConfiguration(value, m.modelReturn == screenChat)
	}
	var command tea.Cmd
	m.modelFilter, command = m.modelFilter.Update(key)
	m.modelCursor = 0
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

func (m model) updateChatKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		return m, tea.Quit
	case "ctrl+l":
		if !m.waiting {
			m.resetConversation()
		}
		return m, nil
	case "ctrl+p":
		if !m.waiting {
			if m.runtime != nil {
				m.enterProviderList()
				return m, nil
			}
			m.enterSetup(m.config)
			return m, m.setup[m.setupFocus].Focus()
		}
		return m, nil
	case "ctrl+s":
		return m.submitChat()
	case "enter":
		return m.deferChatSubmit()
	}
	var commands []tea.Cmd
	var command tea.Cmd
	m.viewport, command = m.viewport.Update(key)
	commands = append(commands, command)
	if !m.waiting {
		m.input, command = m.input.Update(key)
		commands = append(commands, command)
	}
	return m, tea.Batch(commands...)
}

func (m model) deferChatSubmit() (tea.Model, tea.Cmd) {
	if m.waiting || m.submitPending || m.client == nil {
		return m, nil
	}
	m.submitPending = true
	return m, tea.Tick(imeCommitGracePeriod, func(time.Time) tea.Msg {
		return deferredSubmitMsg{}
	})
}

func (m model) submitChat() (tea.Model, tea.Cmd) {
	m.submitPending = false
	content := strings.TrimSpace(norm.NFC.String(m.input.Value()))
	if m.waiting || content == "" || m.client == nil {
		return m, nil
	}
	switch content {
	case "/clear":
		m.input.Reset()
		m.resetConversation()
		return m, m.input.Focus()
	case "/model":
		m.input.Reset()
		return m.discoverCurrentModels()
	case "/provider":
		m.input.Reset()
		if m.runtime != nil {
			m.enterProviderList()
			return m, nil
		}
		m.enterSetup(m.config)
		return m, m.setup[m.setupFocus].Focus()
	}
	userMessage := client.Message{Role: client.RoleUser, Content: content}
	m.messages = append(m.messages, userMessage)
	if m.memory == nil {
		m.memory = memory.New(memoryPolicy(m.config), nil)
	}
	m.memory.Append(userMessage)
	m.pendingMessage = userMessage
	m.input.Reset()
	m.input.Blur()
	m.waiting = true
	m.refreshTranscript()
	if m.memory.ShouldCompact() {
		plan, err := m.memory.Plan()
		if err != nil {
			m.rollbackPendingMessage()
			m.status = err.Error()
			return m, m.input.Focus()
		}
		m.compacting = true
		m.status = "Compacting context…"
		return m, tea.Batch(m.spinner.Tick, m.compactContext(plan))
	}
	m.status = "Thinking…"
	return m, tea.Batch(m.spinner.Tick, m.sendChatRequest())
}

func (m model) compactContext(plan memory.Plan) tea.Cmd {
	configuredClient := m.client
	maxTokens := plan.OutputBudget
	return func() tea.Msg {
		response, err := configuredClient.Chat(m.ctx, client.ChatRequest{
			Messages: plan.RequestMessages(), MaxCompletionTokens: &maxTokens,
		})
		return compactionResultMsg{response: response, plan: plan, err: err}
	}
}

func (m *model) sendChatRequest() tea.Cmd {
	history := m.memory.Messages()
	m.requestEstimate = memory.CountMessages(history)
	conversationID := m.conversationID
	configuredClient := m.client
	return func() tea.Msg {
		response, err := configuredClient.Chat(m.ctx, client.ChatRequest{
			Messages: history, ConversationID: conversationID,
		})
		return chatResultMsg{response: response, err: err}
	}
}

func (m *model) rollbackPendingMessage() {
	if m.pendingMessage.Content != "" {
		if len(m.messages) > 0 {
			m.messages = m.messages[:len(m.messages)-1]
		}
		if m.memory != nil {
			m.memory.PopLast()
		}
		m.input.SetValue(m.pendingMessage.Content)
	}
	m.pendingMessage = client.Message{}
	m.waiting = false
	m.compacting = false
	m.compactionTarget = 0
	m.submitPending = false
	m.refreshTranscript()
}

func (m *model) enterChat(value config.Config, configuredClient chatClient) {
	m.screen = screenChat
	m.setupEdit = false
	m.config = value
	m.client = configuredClient
	m.messages = nil
	if value.Provider.SystemPrompt != "" {
		m.messages = append(m.messages, client.Message{Role: client.RoleSystem, Content: value.Provider.SystemPrompt})
	}
	m.memory = memory.New(memoryPolicy(value), m.messages)
	m.conversationID = ""
	m.waiting = false
	m.compacting = false
	m.compactionTarget = 0
	m.submitPending = false
	m.pendingMessage = client.Message{}
	m.status = ""
	m.input = newChatInput()
	m.resize(m.width, m.height)
	m.input.Focus()
	m.refreshTranscript()
}

func (m *model) enterSetup(value config.Config) {
	m.screen = screenSetup
	m.setupEdit = true
	m.discovering = false
	m.setupFocus = setupBaseURL
	m.setup[setupBaseURL].SetValue(value.Provider.BaseURL)
	m.setup[setupAPIKeyEnv].SetValue(value.Provider.APIKeyEnv)
	m.setup[setupAPIKey].SetValue(value.Provider.APIKey)
	m.input.Blur()
	for index := range m.setup {
		m.setup[index].Blur()
	}
	m.status = ""
}

func (m *model) enterProviderList() {
	m.screen = screenProviders
	m.gatewayConfig = m.runtime.Config()
	if m.providerCursor >= len(m.gatewayConfig.Providers) {
		m.providerCursor = max(0, len(m.gatewayConfig.Providers)-1)
	}
	m.discovering = false
	m.input.Blur()
	for index := range m.setup {
		m.setup[index].Blur()
	}
	if len(m.gatewayConfig.Providers) == 0 {
		m.enterProviderEditor(-1)
		return
	}
	m.status = ""
}

func (m *model) enterProviderEditor(index int) {
	if index < 0 || index >= len(m.gatewayConfig.Providers) {
		index = -1
	}
	m.screen = screenSetup
	m.setupEdit = index >= 0
	m.providerAdding = index < 0
	m.providerEditIndex = index
	m.setupFocus = setupProviderID
	provider := gateway.ProviderConfig{
		ID: "provider", Type: "openai-compatible", Enabled: true,
		BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY",
	}
	if index >= 0 && index < len(m.gatewayConfig.Providers) {
		provider = m.gatewayConfig.Providers[index]
	}
	m.setup[setupProviderID].SetValue(provider.ID)
	m.setup[setupProviderPrefix].SetValue(provider.Prefix)
	m.setProviderType(provider.Type)
	m.setup[setupBaseURL].SetValue(provider.BaseURL)
	m.setup[setupAPIKeyEnv].SetValue(provider.APIKeyEnv)
	m.setup[setupAPIKey].SetValue(provider.APIKey)
	m.input.Blur()
	for fieldIndex := range m.setup {
		m.setup[fieldIndex].Blur()
	}
	m.setup[m.setupFocus].Focus()
	m.status = ""
}

func (m *model) enterModelPicker(value config.Config, available []client.Model) {
	byID := make(map[string]client.Model, len(available))
	for _, candidate := range available {
		if id := strings.TrimSpace(candidate.ID); id != "" {
			candidate.ID = id
			byID[id] = candidate
		}
	}
	m.models = make([]client.Model, 0, len(byID))
	for _, candidate := range byID {
		m.models = append(m.models, candidate)
	}
	sort.Slice(m.models, func(i, j int) bool { return m.models[i].ID < m.models[j].ID })
	if len(m.models) == 0 {
		m.status = "provider returned no models"
		m.screen = m.modelReturn
		return
	}
	m.screen = screenModels
	m.draftConfig = value
	m.modelCursor = 0
	m.modelFilter.Reset()
	m.status = ""
	if m.setupEdit || m.modelReturn == screenChat {
		for index, candidate := range m.models {
			if candidate.ID == m.config.Provider.Model {
				m.modelCursor = index
				break
			}
		}
	}
	m.modelFilter.Focus()
}

func (m model) filteredModels() []client.Model {
	query := strings.ToLower(strings.TrimSpace(m.modelFilter.Value()))
	if query == "" {
		return m.models
	}
	filtered := make([]client.Model, 0, len(m.models))
	for _, candidate := range m.models {
		if strings.Contains(strings.ToLower(candidate.ID), query) {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func (m *model) resetConversation() {
	m.messages = nil
	if m.config.Provider.SystemPrompt != "" {
		m.messages = append(m.messages, client.Message{Role: client.RoleSystem, Content: m.config.Provider.SystemPrompt})
	}
	if m.memory == nil {
		m.memory = memory.New(memoryPolicy(m.config), m.messages)
	} else {
		m.memory.Reset(m.messages)
	}
	m.conversationID = ""
	m.compactionTarget = 0
	m.submitPending = false
	m.status = "Conversation cleared"
	m.refreshTranscript()
}

func (m *model) resize(width, height int) {
	m.width, m.height = max(width, 40), max(height, 12)
	contentWidth := max(20, m.width-4)
	for index := range m.setup {
		m.setup[index].SetWidth(min(contentWidth-2, 72))
	}
	m.modelFilter.SetWidth(min(contentWidth-2, 72))
	m.input.SetWidth(contentWidth)
	m.viewport.SetWidth(contentWidth)
	m.viewport.SetHeight(max(3, m.height-10))
	m.refreshTranscript()
}

func (m *model) applyColorScheme(dark bool) {
	for index := range m.setup {
		m.setup[index].SetStyles(textinput.DefaultStyles(dark))
	}
	m.modelFilter.SetStyles(textinput.DefaultStyles(dark))
	inputStyles := textarea.DefaultStyles(dark)
	inputStyles.Cursor.Shape = tea.CursorBar
	m.input.SetStyles(inputStyles)
}

func (m *model) refreshTranscript() {
	if m.screen != screenChat {
		return
	}
	m.viewport.SetContent(renderTranscript(m.messages, m.viewport.Width()))
	m.viewport.GotoBottom()
}

func renderTranscript(messages []client.Message, width int) string {
	var blocks []string
	for _, message := range messages {
		if message.Role == client.RoleSystem {
			continue
		}
		label := "YOU"
		labelStyle := userLabelStyle
		bodyStyle := userBodyStyle
		if message.Role == client.RoleAssistant {
			label = "ASSISTANT"
			labelStyle = assistantLabelStyle
			bodyStyle = assistantBodyStyle
		}
		body := message.TextContent()
		if body == "" && len(message.ToolCalls) > 0 {
			body = fmt.Sprintf("[%d tool call(s)]", len(message.ToolCalls))
		}
		blocks = append(blocks, labelStyle.Render(label)+"\n"+bodyStyle.Width(max(10, width-2)).Render(body))
	}
	if len(blocks) == 0 {
		return emptyStyle.Render("Start a conversation. Your messages stay in memory for this run.")
	}
	return strings.Join(blocks, "\n\n")
}

func (m model) View() tea.View {
	content := m.viewSetup()
	if m.screen == screenProviders {
		content = m.viewProviders()
	} else if m.screen == screenModels {
		content = m.viewModels()
	} else if m.screen == screenChat {
		content = m.viewChat()
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "q"
	if m.screen == screenChat && !m.waiting {
		if cursor := m.input.Cursor(); cursor != nil {
			cursor.Position.X += frameStyle.GetPaddingLeft()
			cursor.Position.Y += frameStyle.GetPaddingTop() + 2 + m.viewport.Height()
			view.Cursor = cursor
		}
	}
	return view
}

func normalizeTextInputMessage(message tea.Msg) tea.Msg {
	switch value := message.(type) {
	case tea.KeyPressMsg:
		if value.Text != "" && !norm.NFC.IsNormalString(value.Text) {
			value.Text = norm.NFC.String(value.Text)
		}
		return value
	case tea.PasteMsg:
		if !norm.NFC.IsNormalString(value.Content) {
			value.Content = norm.NFC.String(value.Content)
		}
		return value
	default:
		return message
	}
}

func (m model) viewSetup() string {
	labels := []string{"Provider ID", "Model prefix", "API type", "Base URL", "API key environment variable", "API key (optional, stored in config)"}
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
	}
	body.WriteString(helpStyle.Render(help))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewProviders() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · providers"))
	body.WriteString("\n")
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
			body.WriteString(subtleStyle.Render("  " + provider.Type + " · prefix " + modelPrefix + " · " + state))
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
	body.WriteString(helpStyle.Render("↑/↓ select · enter edit · a add · space enable/disable · d delete · esc chat"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewModels() string {
	filtered := m.filteredModels()
	visible := max(4, m.height-10)
	start := 0
	if m.modelCursor >= visible {
		start = m.modelCursor - visible + 1
	}
	end := min(len(filtered), start+visible)

	var body strings.Builder
	body.WriteString(titleStyle.Render("q · select model"))
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
			body.WriteString("\n")
		}
	}
	body.WriteString("\n")
	if m.discovering {
		body.WriteString(helpStyle.Render("loading models · ctrl+c quit"))
	} else {
		body.WriteString(helpStyle.Render(fmt.Sprintf("%d/%d models · type to filter · ↑/↓ select · enter save · esc back", len(filtered), len(m.models))))
	}
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewChat() string {
	endpoint := m.config.Provider.BaseURL
	if m.runtime != nil {
		endpoint = m.runtime.Endpoint()
	}
	header := titleStyle.Render("q") + "  " + subtleStyle.Render(m.config.Provider.Model+" · "+endpoint+" · "+m.contextLabel())
	status := m.status
	if m.waiting {
		status = m.spinner.View() + " " + status
	}
	footer := helpStyle.Render("enter send · shift+enter newline · /clear · /model · /provider · esc quit")
	content := header + "\n\n" + m.viewport.View() + "\n" + m.input.View()
	if status != "" {
		content += "\n" + subtleStyle.Render(status)
	}
	content += "\n" + footer
	return frameStyle.Width(max(36, m.width-4)).Render(content)
}

func memoryPolicy(value config.Config) memory.Policy {
	contextConfig := value.EffectiveContext()
	return memory.Policy{
		ContextWindow: int(value.EffectiveContextWindow()),
		TriggerRatio:  contextConfig.TriggerRatio,
		TargetRatio:   contextConfig.TargetRatio,
		RecentRatio:   contextConfig.RecentRatio,
	}
}

func (m model) contextLabel() string {
	if m.memory == nil {
		return "context unknown"
	}
	stats := m.memory.Stats()
	if stats.ContextWindow <= 0 {
		return "context unknown"
	}
	percent := min(999, stats.PredictedTokens*100/stats.ContextWindow)
	return fmt.Sprintf("context %d%% · %s/%s", percent, formatTokens(stats.PredictedTokens), formatTokens(stats.ContextWindow))
}

func formatTokens(tokens int) string {
	if tokens >= 1_000_000 {
		return fmt.Sprintf("%.1fm", float64(tokens)/1_000_000)
	}
	if tokens >= 1_000 {
		return fmt.Sprintf("%.1fk", float64(tokens)/1_000)
	}
	return fmt.Sprintf("%d", tokens)
}

var (
	titleStyle          = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	subtleStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	helpStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errorStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	activeLabelStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	userLabelStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	assistantLabelStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213"))
	userBodyStyle       = lipgloss.NewStyle().PaddingLeft(1)
	assistantBodyStyle  = lipgloss.NewStyle().PaddingLeft(1)
	emptyStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true)
	frameStyle          = lipgloss.NewStyle().Padding(1, 2)
)
