package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/workspace"
	"golang.org/x/text/unicode/norm"
)

type screen uint8

const (
	screenSetup screen = iota
	screenProviders
	screenModels
	screenChat
)

type modelPickerStage uint8

const (
	modelPickerModels modelPickerStage = iota
	modelPickerTargets
	modelPickerReasoning
	modelPickerEmbeddingDimensions
	modelPickerContextWindow
)

const (
	defaultModelTarget   = "default"
	embeddingModelTarget = "embedding"
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
	ctx         context.Context
	store       config.Store
	factory     clientFactory
	screen      screen
	width       int
	height      int
	dark        bool
	status      string
	runtime     providerRuntime
	toolRuntime agentToolRuntime

	workspaceStore    *workspace.Store
	workspaceRestored bool
	archive           recordArchive
	archiveErr        error
	runID             string

	setup               [setupFieldCount]textinput.Model
	setupFocus          int
	setupEdit           bool
	providerEditIndex   int
	providerAdding      bool
	providerTypeCursor  int
	gatewayConfig       gateway.Config
	providerCursor      int
	discovering         bool
	models              []client.Model
	modelCursor         int
	modelFilter         textinput.Model
	modelPickerStage    modelPickerStage
	modelChooseTarget   bool
	modelTarget         string
	modelTargetCursor   int
	reasoningCursor     int
	modelSelection      client.Model
	embeddingDimensions textinput.Model
	modelContextWindow  textinput.Model
	draftConfig         config.Config
	modelReturn         screen

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

type modelTargetConfiguredMsg struct {
	config config.Config
	target string
	err    error
}

type chatResultMsg struct {
	response        *client.ChatResponse
	intermediate    []client.Message
	requestEstimate int
	toolCalls       int
	err             error
}

type agentEvent struct {
	message         *client.Message
	call            *client.ToolCall
	toolIsError     bool
	response        *client.ChatResponse
	requestEstimate int
	toolCalls       int
	err             error
}

type agentEventMsg struct {
	events <-chan agentEvent
	event  agentEvent
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
	modelTarget   string
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
	m.embeddingDimensions = textinput.New()
	m.embeddingDimensions.Prompt = "dimensions · "
	m.embeddingDimensions.Placeholder = "1536"
	m.embeddingDimensions.CharLimit = 5
	m.embeddingDimensions.SetWidth(24)
	m.modelContextWindow = textinput.New()
	m.modelContextWindow.Prompt = "context length · "
	m.modelContextWindow.Placeholder = "provider metadata"
	m.modelContextWindow.CharLimit = 12
	m.modelContextWindow.SetWidth(28)
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
		return tea.Batch(m.modelPickerFocus(), tea.RequestBackgroundColor)
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
	case modelTargetConfiguredMsg:
		if message.err != nil {
			m.status = message.err.Error()
			return m, m.modelPickerFocus()
		}
		m.config = message.config
		m.draftConfig = message.config
		m.screen = screenModels
		m.modelPickerStage = modelPickerTargets
		m.status = message.target + " model settings saved"
		m.modelFilter.Blur()
		m.embeddingDimensions.Blur()
		return m, nil
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
		return m, m.modelPickerFocus()
	case providersAppliedMsg:
		m.discovering = false
		if message.err != nil {
			m.status = message.err.Error()
			if m.screen == screenProviders {
				return m, nil
			}
			if m.screen == screenModels {
				return m, m.modelPickerFocus()
			}
			return m, m.setup[m.setupFocus].Focus()
		}
		if m.client != nil && m.client != message.client {
			_ = m.client.Close()
		}
		m.client = message.client
		m.gatewayConfig = message.gatewayConfig
		if strings.TrimSpace(m.config.Provider.Model) == "" || m.config.Provider.Model == "model-discovery" {
			m.config = message.config
		} else {
			m.config.Provider.ContextWindow = message.config.Provider.ContextWindow
			if m.memory != nil {
				m.memory.Configure(memoryPolicy(m.config))
			}
		}
		m.conversationID = ""
		m.compactionTarget = 0
		m.enterModelPicker(message.config, message.models)
		if message.modelTarget != "" {
			m.modelChooseTarget = true
			m.modelPickerStage = modelPickerModels
			m.modelTarget = message.modelTarget
			m.selectConfiguredModel()
		}
		return m, m.modelPickerFocus()
	case agentEventMsg:
		event := message.event
		if event.call != nil {
			m.archiveToolCall(*event.call)
			m.status = "Running tool · " + describeToolCall(*event.call)
			return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events))
		}
		if event.message != nil {
			m.messages = append(m.messages, *event.message)
			if m.memory != nil {
				m.memory.Append(*event.message)
			}
			if event.message.Role == client.RoleAssistant {
				m.archiveMessage(*event.message, sessionstore.StatusSucceeded, false)
				m.status = "Preparing tool call…"
			} else {
				status := sessionstore.StatusSucceeded
				if event.toolIsError {
					status = sessionstore.StatusFailed
				}
				m.archiveMessage(*event.message, status, event.toolIsError)
				m.status = "Thinking… · " + event.message.Name + " completed"
			}
			m.refreshTranscript()
			return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events))
		}
		return m.Update(chatResultMsg{
			response: event.response, requestEstimate: event.requestEstimate,
			toolCalls: event.toolCalls, err: event.err,
		})
	case chatResultMsg:
		m.waiting = false
		m.compacting = false
		m.pendingMessage = client.Message{}
		if message.err != nil {
			m.compactionTarget = 0
			m.archiveFailure("chat", message.err)
			if archiveErr := m.flushArchive(); archiveErr != nil {
				m.status = message.err.Error() + " · archive: " + archiveErr.Error()
				return m, m.input.Focus()
			}
			m.status = message.err.Error()
			return m, m.input.Focus()
		}
		if message.response == nil || len(message.response.Choices) == 0 {
			m.compactionTarget = 0
			err := errors.New("provider returned no choices")
			m.archiveFailure("chat", err)
			m.status = "provider returned no choices"
			if archiveErr := m.flushArchive(); archiveErr != nil {
				m.status += " · archive: " + archiveErr.Error()
			}
			return m, m.input.Focus()
		}
		assistant := message.response.Choices[0].Message
		if assistant.Role == "" {
			assistant.Role = client.RoleAssistant
		}
		if message.response.ConversationID != "" {
			m.conversationID = message.response.ConversationID
		}
		m.messages = append(m.messages, message.intermediate...)
		m.messages = append(m.messages, assistant)
		for _, intermediate := range message.intermediate {
			m.archiveMessage(intermediate, sessionstore.StatusSucceeded, false)
		}
		m.archiveMessage(assistant, sessionstore.StatusSucceeded, false)
		if m.memory != nil {
			for _, intermediate := range message.intermediate {
				m.memory.Append(intermediate)
			}
			requestEstimate := message.requestEstimate
			if requestEstimate == 0 {
				requestEstimate = m.requestEstimate
			}
			m.memory.ObserveUsage(message.response.Usage.PromptTokens, requestEstimate)
			m.memory.Append(assistant)
		}
		if m.compactionTarget > 0 && message.response.Usage.PromptTokens > m.compactionTarget {
			m.status = fmt.Sprintf("Context remains above target · %s/%s", formatTokens(message.response.Usage.PromptTokens), formatTokens(m.compactionTarget))
		} else {
			m.status = ""
			if message.toolCalls > 0 {
				m.status = fmt.Sprintf("Tools used · %d", message.toolCalls)
			}
		}
		m.compactionTarget = 0
		m.refreshTranscript()
		if err := m.saveWorkspaceSession(); err != nil {
			m.status = err.Error()
		}
		if err := m.flushArchive(); err != nil {
			m.status = "archive: " + err.Error()
		}
		return m, m.input.Focus()
	case compactionResultMsg:
		if message.err != nil {
			m.rollbackPendingMessage()
			m.archiveFailure("context_compaction", message.err)
			m.status = "compact context: " + message.err.Error()
			if archiveErr := m.flushArchive(); archiveErr != nil {
				m.status += " · archive: " + archiveErr.Error()
			}
			return m, m.input.Focus()
		}
		if message.response == nil || len(message.response.Choices) == 0 {
			m.rollbackPendingMessage()
			err := errors.New("provider returned no choices")
			m.archiveFailure("context_compaction", err)
			m.status = "compact context: provider returned no choices"
			if archiveErr := m.flushArchive(); archiveErr != nil {
				m.status += " · archive: " + archiveErr.Error()
			}
			return m, m.input.Focus()
		}
		summary := strings.TrimSpace(message.response.Choices[0].Message.TextContent())
		if err := m.memory.Apply(message.plan, summary); err != nil {
			m.rollbackPendingMessage()
			m.archiveFailure("context_compaction", err)
			m.status = "compact context: " + err.Error()
			if archiveErr := m.flushArchive(); archiveErr != nil {
				m.status += " · archive: " + archiveErr.Error()
			}
			return m, m.input.Focus()
		}
		m.conversationID = ""
		m.archiveSummary(summary)
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
	returnTarget := ""
	if m.screen == screenModels && m.modelPickerStage == modelPickerContextWindow {
		returnTarget = m.modelTarget
	} else {
		m.modelChooseTarget = false
	}
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
		if refreshed, found := refreshModelContextWindow(value, models); found {
			value = refreshed
			if err := m.store.Save(value); err != nil {
				_ = configuredClient.Close()
				return providersAppliedMsg{err: err}
			}
		}
		return providersAppliedMsg{
			models: models, config: value, gatewayConfig: candidate, client: configuredClient,
			modelTarget: returnTarget,
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
	for index := range result.Providers {
		source := value.Providers[index].ModelMetadata
		if source == nil {
			continue
		}
		result.Providers[index].ModelMetadata = make(map[string]client.ModelMetadata, len(source))
		for modelID, metadata := range source {
			result.Providers[index].ModelMetadata[modelID] = metadata
		}
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

func (m model) updateModelPicker(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.discovering {
		return m, nil
	}
	switch m.modelPickerStage {
	case modelPickerTargets:
		return m.updateModelTargetPicker(key)
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
			m.modelPickerStage = modelPickerTargets
			m.modelFilter.Blur()
			m.status = ""
			return m, nil
		}
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
	case "ctrl+e":
		if len(filtered) == 0 {
			return m, nil
		}
		return m.enterContextWindowPicker(filtered[m.modelCursor])
	case "enter":
		if len(filtered) == 0 {
			return m, nil
		}
		value := m.draftConfig
		selected := filtered[m.modelCursor]
		if m.modelTarget == "" || m.modelTarget == defaultModelTarget {
			value.Provider.Model = selected.ID
			value.Provider.ContextWindow = selected.ContextLength
			return m.saveConfiguration(value, m.modelReturn == screenChat)
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
		value = withAgentModel(value, m.modelTarget, selected.ID)
		m.draftConfig = value
		m.modelSelection = selected
		reasoning := selectedReasoning(selected)
		if reasoning == nil || len(reasoning.SupportedEfforts) == 0 {
			value = withAgentReasoningEffort(value, m.modelTarget, "")
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
	targets := modelTargets()
	switch key.String() {
	case "esc":
		m.screen = m.modelReturn
		m.status = ""
		if m.screen == screenChat {
			return m, m.input.Focus()
		}
		return m, m.setup[m.setupFocus].Focus()
	case "up":
		if m.modelTargetCursor > 0 {
			m.modelTargetCursor--
		}
	case "down":
		if m.modelTargetCursor < len(targets)-1 {
			m.modelTargetCursor++
		}
	case "enter":
		if len(targets) == 0 {
			return m, nil
		}
		m.modelTarget = targets[m.modelTargetCursor]
		m.modelPickerStage = modelPickerModels
		m.modelFilter.Reset()
		m.selectConfiguredModel()
		return m, m.modelFilter.Focus()
	case "i":
		if len(targets) == 0 || targets[m.modelTargetCursor] == defaultModelTarget {
			return m, nil
		}
		target := targets[m.modelTargetCursor]
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
		value := withAgentReasoningEffort(m.draftConfig, m.modelTarget, options[m.reasoningCursor])
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
	m.archiveMessage(userMessage, sessionstore.StatusSubmitted, false)
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
			m.archiveFailure("context_compaction", err)
			m.status = err.Error()
			if archiveErr := m.flushArchive(); archiveErr != nil {
				m.status += " · archive: " + archiveErr.Error()
			}
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
	toolRuntime := m.toolRuntime
	if toolRuntime == nil {
		return func() tea.Msg {
			response, err := configuredClient.Chat(m.ctx, client.ChatRequest{
				Messages: providerMessages(history), ConversationID: conversationID,
			})
			return chatResultMsg{response: response, requestEstimate: m.requestEstimate, err: err}
		}
	}
	events := make(chan agentEvent)
	return func() tea.Msg {
		go streamAgentLoop(m.ctx, configuredClient, toolRuntime, history, conversationID, events)
		return waitAgentEvent(events)()
	}
}

const maximumToolRounds = 32

func streamAgentLoop(
	ctx context.Context,
	configuredClient chatClient,
	toolRuntime agentToolRuntime,
	history []client.Message,
	conversationID string,
	events chan<- agentEvent,
) {
	defer close(events)
	availableTools := toolRuntime.Tools()
	toolCalls := 0
	for round := 0; round < maximumToolRounds; round++ {
		requestEstimate := memory.CountMessages(history)
		response, err := configuredClient.Chat(ctx, client.ChatRequest{
			Messages: providerMessages(history), ConversationID: conversationID, Tools: availableTools,
		})
		if err != nil {
			emitAgentEvent(ctx, events, agentEvent{err: err})
			return
		}
		if response == nil || len(response.Choices) == 0 {
			emitAgentEvent(ctx, events, agentEvent{response: response})
			return
		}
		assistant := response.Choices[0].Message
		if assistant.Role == "" {
			assistant.Role = client.RoleAssistant
		}
		if len(assistant.ToolCalls) == 0 {
			response.Choices[0].Message = assistant
			if response.ConversationID == "" {
				response.ConversationID = conversationID
			}
			emitAgentEvent(ctx, events, agentEvent{
				response: response, requestEstimate: requestEstimate, toolCalls: toolCalls,
			})
			return
		}
		if response.ConversationID != "" {
			conversationID = response.ConversationID
		}
		for index := range assistant.ToolCalls {
			if assistant.ToolCalls[index].ID == "" {
				assistant.ToolCalls[index].ID = fmt.Sprintf("q-call-%d-%d", round+1, index+1)
			}
		}
		history = append(history, assistant)
		if !emitAgentEvent(ctx, events, agentEvent{message: &assistant}) {
			return
		}
		for _, call := range assistant.ToolCalls {
			callCopy := call
			if !emitAgentEvent(ctx, events, agentEvent{call: &callCopy}) {
				return
			}
			result, callErr := toolRuntime.Call(ctx, call)
			if callErr != nil {
				result = client.ToolResult{Content: callErr.Error(), IsError: true}
			}
			content := result.Content
			if result.IsError {
				content = "Tool error: " + content
			}
			message := client.Message{
				Role: client.RoleTool, Name: call.Function.Name,
				ToolCallID: call.ID, Content: content,
			}
			history = append(history, message)
			toolCalls++
			if !emitAgentEvent(ctx, events, agentEvent{message: &message, toolIsError: result.IsError}) {
				return
			}
		}
	}
	emitAgentEvent(ctx, events, agentEvent{err: fmt.Errorf("agent stopped after %d tool rounds", maximumToolRounds)})
}

func emitAgentEvent(ctx context.Context, events chan<- agentEvent, event agentEvent) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func waitAgentEvent(events <-chan agentEvent) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			event.err = errors.New("agent event stream closed unexpectedly")
		}
		return agentEventMsg{events: events, event: event}
	}
}

// providerMessages removes q-internal message names that some compatible APIs
// reject outside the user role. Names remain in memory for summary detection
// and transcript labels, but are not part of the provider wire request.
func providerMessages(messages []client.Message) []client.Message {
	result := append([]client.Message(nil), messages...)
	for index := range result {
		if result[index].Role != client.RoleUser {
			result[index].Name = ""
		}
	}
	return result
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
	if m.toolRuntime != nil && m.workspaceStore != nil {
		environment := m.toolRuntime.Environment()
		workspacePrompt := fmt.Sprintf(
			"Runtime environment: OS=%s; architecture=%s; run_command shell=%s. Use commands and quoting compatible with this shell. ",
			environment.OS, environment.Architecture, environment.Shell,
		) + "Current workspace root: " + filepath.Clean(m.workspaceStore.Root) +
			". Use the available tools to inspect, edit, and run work in this workspace when the user asks for changes." +
			" Non-Loom MCP tool results include a loom_ref to the immutable full result. For large results, use loom_inspect, loom_read, or loom_eval instead of copying the result through chat context."
		if m.archive != nil {
			workspacePrompt += " Use search_archive when prior workspace conversations, decisions, agent results, or tool failures may be relevant; use get_archive_record only for selected results that need more detail."
		}
		m.messages = append(m.messages, client.Message{
			Role: client.RoleDeveloper, Name: "q_workspace",
			Content: workspacePrompt,
		})
	}
	m.memory = memory.New(memoryPolicy(value), m.messages)
	m.conversationID = ""
	m.runID = ""
	m.waiting = false
	m.compacting = false
	m.compactionTarget = 0
	m.submitPending = false
	m.pendingMessage = client.Message{}
	m.status = ""
	m.input = newChatInput()
	m.restoreWorkspaceSession()
	m.ensureRunID()
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
	if m.modelChooseTarget {
		m.modelPickerStage = modelPickerTargets
		m.modelTarget = defaultModelTarget
		m.modelTargetCursor = 0
		m.modelFilter.Blur()
		return
	}
	m.modelPickerStage = modelPickerModels
	m.modelTarget = defaultModelTarget
	m.selectConfiguredModel()
	m.modelFilter.Focus()
}

func (m *model) selectConfiguredModel() {
	modelID := m.draftConfig.Provider.Model
	if m.modelTarget == embeddingModelTarget {
		modelID = m.draftConfig.Embedding.Model
	} else if m.modelTarget != "" && m.modelTarget != defaultModelTarget {
		if agent, ok := m.draftConfig.Agents.Roles[m.modelTarget]; ok && agent.Model != "" {
			modelID = agent.Model
		}
	}
	m.modelCursor = 0
	for index, candidate := range m.models {
		if candidate.ID == modelID {
			m.modelCursor = index
			return
		}
	}
}

func (m *model) modelPickerFocus() tea.Cmd {
	switch {
	case m.modelPickerStage == modelPickerModels && !m.discovering:
		return m.modelFilter.Focus()
	case m.modelPickerStage == modelPickerEmbeddingDimensions && !m.discovering:
		return m.embeddingDimensions.Focus()
	case m.modelPickerStage == modelPickerContextWindow && !m.discovering:
		return m.modelContextWindow.Focus()
	}
	m.modelFilter.Blur()
	m.embeddingDimensions.Blur()
	m.modelContextWindow.Blur()
	return nil
}

func modelTargets() []string {
	return append([]string{defaultModelTarget, embeddingModelTarget}, config.AgentRoles()...)
}

func withAgentModel(value config.Config, role, modelID string) config.Config {
	value.Agents.Roles = cloneAgentRoles(value.Agents.Roles)
	agent := value.Agents.Roles[role]
	agent.Model = modelID
	value.Agents.Roles[role] = agent
	return value
}

func withAgentReasoningEffort(value config.Config, role, effort string) config.Config {
	value.Agents.Roles = cloneAgentRoles(value.Agents.Roles)
	agent := value.Agents.Roles[role]
	agent.ReasoningEffort = effort
	value.Agents.Roles[role] = agent
	return value
}

func withoutAgentOverride(value config.Config, role string) config.Config {
	value.Agents.Roles = cloneAgentRoles(value.Agents.Roles)
	delete(value.Agents.Roles, role)
	return value
}

func cloneAgentRoles(source map[string]config.AgentConfig) map[string]config.AgentConfig {
	result := make(map[string]config.AgentConfig, len(source)+1)
	for role, agent := range source {
		result[role] = agent
	}
	return result
}

func selectedReasoning(model client.Model) *client.ReasoningCapabilities {
	if model.Capabilities == nil || model.Capabilities.Reasoning == nil {
		return nil
	}
	reasoning := model.Capabilities.Reasoning
	if !reasoning.Supported || reasoning.Control != client.ReasoningControlEffort {
		return nil
	}
	return reasoning
}

func reasoningEffortCursor(value config.Config, role string, efforts []string) int {
	configured := value.Agents.Roles[role].ReasoningEffort
	for index, effort := range efforts {
		if configured == effort {
			return index + 1
		}
	}
	return 0
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
	m.runID = ""
	m.ensureRunID()
	m.compactionTarget = 0
	m.submitPending = false
	m.status = "Conversation cleared"
	if m.workspaceStore != nil {
		if err := m.workspaceStore.Clear(); err != nil {
			m.status = err.Error()
		}
	}
	m.refreshTranscript()
}

func (m *model) restoreWorkspaceSession() {
	if m.workspaceStore == nil || m.workspaceRestored {
		return
	}
	m.workspaceRestored = true
	session, err := m.workspaceStore.Load()
	if errors.Is(err, workspace.ErrNotFound) {
		return
	}
	if err != nil {
		m.status = err.Error()
		return
	}
	base := append([]client.Message(nil), m.messages...)
	m.messages = mergeWorkspaceMessages(base, session.Transcript)
	requestContext := session.Context
	if len(requestContext) == 0 {
		requestContext = session.Transcript
	}
	m.memory = memory.New(memoryPolicy(m.config), mergeWorkspaceMessages(base, requestContext))
	m.runID = session.RunID
	m.ensureRunID()
	if len(session.Transcript) > 0 {
		m.status = fmt.Sprintf("Workspace session restored · %d messages", len(session.Transcript))
	}
}

func (m *model) saveWorkspaceSession() error {
	if m.workspaceStore == nil || m.memory == nil {
		return nil
	}
	return m.workspaceStore.Save(workspace.Session{
		RunID:      m.runID,
		Transcript: workspaceSessionMessages(m.messages),
		Context:    workspaceSessionMessages(m.memory.Messages()),
	})
}

func workspaceSessionMessages(messages []client.Message) []client.Message {
	saved := make([]client.Message, 0, len(messages))
	for _, message := range messages {
		if (message.Role == client.RoleSystem || message.Role == client.RoleDeveloper) && message.Name != memory.SummaryName {
			continue
		}
		saved = append(saved, message)
	}
	return saved
}

func mergeWorkspaceMessages(base, saved []client.Message) []client.Message {
	merged := append([]client.Message(nil), base...)
	for _, message := range saved {
		if (message.Role == client.RoleSystem || message.Role == client.RoleDeveloper) && message.Name != memory.SummaryName {
			continue
		}
		merged = append(merged, message)
	}
	return merged
}

func (m *model) resize(width, height int) {
	m.width, m.height = max(width, 40), max(height, 12)
	contentWidth := max(20, m.width-4)
	for index := range m.setup {
		m.setup[index].SetWidth(min(contentWidth-2, 72))
	}
	m.modelFilter.SetWidth(min(contentWidth-2, 72))
	m.embeddingDimensions.SetWidth(min(contentWidth-2, 24))
	m.modelContextWindow.SetWidth(min(contentWidth-2, 28))
	m.input.SetWidth(contentWidth)
	m.viewport.SetWidth(contentWidth)
	chromeHeight := 10
	if m.workspaceStore != nil {
		chromeHeight++
	}
	m.viewport.SetHeight(max(3, m.height-chromeHeight))
	m.refreshTranscript()
}

func (m *model) applyColorScheme(dark bool) {
	m.dark = dark
	for index := range m.setup {
		m.setup[index].SetStyles(textinput.DefaultStyles(dark))
	}
	m.modelFilter.SetStyles(textinput.DefaultStyles(dark))
	m.embeddingDimensions.SetStyles(textinput.DefaultStyles(dark))
	m.modelContextWindow.SetStyles(textinput.DefaultStyles(dark))
	inputStyles := textarea.DefaultStyles(dark)
	inputStyles.Cursor.Shape = tea.CursorBar
	m.input.SetStyles(inputStyles)
	m.refreshTranscript()
}

func (m *model) refreshTranscript() {
	if m.screen != screenChat {
		return
	}
	m.viewport.SetContent(renderTranscriptWithStyle(m.messages, m.viewport.Width(), m.dark))
	m.viewport.GotoBottom()
}

func renderTranscript(messages []client.Message, width int) string {
	return renderTranscriptWithStyle(messages, width, true)
}

func renderTranscriptWithStyle(messages []client.Message, width int, dark bool) string {
	bodyWidth := max(10, width-2)
	markdownRenderer, _ := newMarkdownRenderer(bodyWidth, dark)
	var blocks []string
	for _, message := range messages {
		if message.Role == client.RoleSystem || message.Role == client.RoleDeveloper {
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
		if message.Role == client.RoleTool {
			label = "TOOL"
			if message.Name != "" {
				label += " · " + message.Name
			}
			labelStyle = subtleStyle
			bodyStyle = assistantBodyStyle
		}
		body := message.TextContent()
		if message.Role == client.RoleAssistant && body != "" {
			body = renderMarkdown(markdownRenderer, body)
		}
		if len(message.ToolCalls) > 0 {
			calls := renderToolCalls(message.ToolCalls)
			if body == "" {
				body = calls
			} else {
				body += "\n\n" + calls
			}
		}
		if message.Role == client.RoleTool {
			body = renderToolResult(message)
		}
		blocks = append(blocks, labelStyle.Render(label)+"\n"+bodyStyle.Width(bodyWidth).Render(body))
	}
	if len(blocks) == 0 {
		return emptyStyle.Render("Start a conversation. Your messages stay in memory for this run.")
	}
	return strings.Join(blocks, "\n\n")
}

func newMarkdownRenderer(width int, dark bool) (*glamour.TermRenderer, error) {
	style := "light"
	if dark {
		style = "dark"
	}
	return glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithWordWrap(max(10, width)),
		glamour.WithPreservedNewLines(),
	)
}

func renderMarkdown(renderer *glamour.TermRenderer, source string) string {
	if renderer == nil {
		return source
	}
	rendered, err := renderer.Render(source)
	if err != nil {
		return source
	}
	return strings.Trim(rendered, "\n")
}

func renderToolCalls(calls []client.ToolCall) string {
	lines := make([]string, 0, len(calls))
	for _, call := range calls {
		lines = append(lines, "→ "+describeToolCall(call))
	}
	return strings.Join(lines, "\n")
}

func describeToolCall(call client.ToolCall) string {
	arguments := toolArguments(call.Function.Arguments)
	switch call.Function.Name {
	case "run_command":
		if command := stringArgument(arguments, "command"); command != "" {
			return "$ " + command
		}
	case "wait":
		return "wait for " + stringArgument(arguments, "command_id")
	case "cmd_status":
		return "check " + stringArgument(arguments, "command_id")
	case "read_file":
		return "read " + stringArgument(arguments, "path")
	case "write_file":
		content := stringArgument(arguments, "content")
		return fmt.Sprintf("write %s · %d bytes", stringArgument(arguments, "path"), len([]byte(content)))
	case "edit_file":
		return "edit " + stringArgument(arguments, "path")
	case "list_directory":
		path := stringArgument(arguments, "path")
		if path == "" {
			path = "."
		}
		return "list " + path
	case "create_directory":
		return "create directory " + stringArgument(arguments, "path")
	case "remove_path":
		return "remove " + stringArgument(arguments, "path")
	case "move_path", "copy_path":
		return call.Function.Name + " " + stringArgument(arguments, "source") + " → " + stringArgument(arguments, "destination")
	}
	return call.Function.Name
}

func renderToolResult(message client.Message) string {
	content := message.Content
	isError := strings.HasPrefix(content, "Tool error: ")
	if isError {
		content = strings.TrimPrefix(content, "Tool error: ")
	}
	var value map[string]any
	if json.Unmarshal([]byte(content), &value) != nil {
		marker := "✓"
		if isError {
			marker = "✗"
		}
		return marker + " " + content
	}
	loomNote := ""
	if ref := stringValue(value["loom_ref"]); ref != "" {
		loomNote = "\n↳ " + ref
		if bytes := stringValue(value["bytes"]); bytes != "" {
			loomNote += " · " + bytes + " bytes"
		}
		if result, exists := value["result"]; exists {
			if resultObject, ok := result.(map[string]any); ok {
				value = resultObject
			} else {
				body, _ := json.MarshalIndent(result, "", "  ")
				marker := "✓"
				if isError {
					marker = "✗"
				}
				return marker + " " + message.Name + "\n" + string(body) + loomNote
			}
		} else {
			marker := "•"
			if isError {
				marker = "✗"
			}
			rendered := marker + " " + message.Name + " · full result stored in Loom"
			if preview := strings.TrimSpace(stringValue(value["preview"])); preview != "" {
				rendered += "\n" + limitToolOutput(preview)
			}
			return rendered + loomNote
		}
	}
	if isError {
		body, _ := json.MarshalIndent(value, "", "  ")
		return "✗ " + message.Name + "\n" + string(body) + loomNote
	}
	var rendered string
	switch message.Name {
	case "run_command", "cmd_status", "wait":
		id := stringValue(value["command_id"])
		status := stringValue(value["status"])
		marker := "•"
		if status == "succeeded" {
			marker = "✓"
		} else if status == "failed" {
			marker = "✗"
		}
		rendered = strings.TrimSpace(strings.Join([]string{marker, id, "·", status}, " "))
		if exitCode, ok := value["exit_code"]; ok {
			rendered += " · exit " + stringValue(exitCode)
		}
		if output := strings.TrimRight(stringValue(value["output"]), "\r\n"); output != "" {
			rendered += "\n" + limitToolOutput(output)
		}
		if more, _ := value["more_output"].(bool); more {
			rendered += "\n… more output available"
		}
	case "write_file":
		rendered = fmt.Sprintf("✓ wrote %s · %s bytes", stringValue(value["path"]), stringValue(value["bytes"]))
	case "edit_file":
		rendered = fmt.Sprintf("✓ edited %s · %s change(s)", stringValue(value["path"]), stringValue(value["applied"]))
		if diff := strings.TrimSpace(stringValue(value["diff"])); diff != "" {
			rendered += "\n" + limitToolOutput(diff)
		}
	case "read_file":
		rendered = fmt.Sprintf("✓ read %s · %s/%s lines", stringValue(value["path"]), stringValue(value["line_count"]), stringValue(value["total_lines"]))
	case "create_directory":
		rendered = "✓ created " + stringValue(value["path"])
	case "remove_path":
		rendered = "✓ removed " + stringValue(value["path"])
	case "move_path", "copy_path":
		rendered = "✓ " + stringValue(value["source"]) + " → " + stringValue(value["destination"])
	case "list_directory":
		entries, _ := value["entries"].([]any)
		rendered = fmt.Sprintf("✓ listed %s · %d entries", stringValue(value["path"]), len(entries))
	default:
		body, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			rendered = "✓ " + message.Name
		} else {
			rendered = "✓ " + message.Name + "\n" + string(body)
		}
	}
	return rendered + loomNote
}

func toolArguments(arguments string) map[string]any {
	var value map[string]any
	_ = json.Unmarshal([]byte(arguments), &value)
	return value
}

func stringArgument(arguments map[string]any, name string) string {
	return stringValue(arguments[name])
}

func stringValue(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}

func limitToolOutput(output string) string {
	const maximumRunes = 8000
	runes := []rune(output)
	if len(runes) <= maximumRunes {
		return output
	}
	return "… output truncated in UI\n" + string(runes[len(runes)-maximumRunes:])
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
			inputOffset := m.renderedChatHeaderHeight() + 1 + lipgloss.Height(m.viewport.View())
			cursor.Position.X += frameStyle.GetPaddingLeft()
			cursor.Position.Y += frameStyle.GetPaddingTop() + inputOffset
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
	switch m.modelPickerStage {
	case modelPickerTargets:
		return m.viewModelTargets()
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
	title := "q · select model"
	if m.modelTarget != "" {
		title = "q · " + m.modelTarget + " model"
	}
	body.WriteString(titleStyle.Render(title))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
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
			if filtered[index].ContextLength > 0 {
				body.WriteString(subtleStyle.Render("  · context " + formatTokenCount(filtered[index].ContextLength)))
			} else {
				body.WriteString(subtleStyle.Render("  · context unknown"))
			}
			if _, overridden := m.gatewayContextWindowOverride(filtered[index].ID); overridden {
				body.WriteString(subtleStyle.Render(" · Gateway override"))
			}
			body.WriteString("\n")
		}
	}
	body.WriteString("\n")
	if m.discovering {
		body.WriteString(helpStyle.Render("loading models · ctrl+c quit"))
	} else {
		help := fmt.Sprintf("%d/%d models · type to filter · ↑/↓ select · ctrl+e context · enter save · esc back", len(filtered), len(m.models))
		if m.modelTarget != "" && m.modelTarget != defaultModelTarget {
			help = fmt.Sprintf("%d/%d models · type to filter · ↑/↓ select · ctrl+e context · enter next/save · esc back", len(filtered), len(m.models))
		}
		body.WriteString(helpStyle.Render(help))
	}
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewModelTargets() string {
	targets := modelTargets()
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · model settings"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render("Choose the main loop, embedding model, or a subagent role"))
	body.WriteString("\n\n")
	if m.discovering {
		body.WriteString(subtleStyle.Render("Loading models…"))
	} else {
		for index, target := range targets {
			prefix := "  "
			style := subtleStyle
			if index == m.modelTargetCursor {
				prefix = "› "
				style = activeLabelStyle
			}
			body.WriteString(prefix)
			body.WriteString(style.Render(target))
			body.WriteString(subtleStyle.Render("  " + targetModelSummary(m.draftConfig, target)))
			body.WriteString("\n")
		}
	}
	if m.status != "" && !m.discovering {
		body.WriteString("\n")
		body.WriteString(subtleStyle.Render(m.status))
		body.WriteString("\n")
	}
	body.WriteString("\n")
	body.WriteString(helpStyle.Render("↑/↓ select · enter edit · i inherit/clear · esc chat"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewReasoningEfforts() string {
	reasoning := selectedReasoning(m.modelSelection)
	efforts := []string{""}
	if reasoning != nil {
		efforts = append(efforts, reasoning.SupportedEfforts...)
	}
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · " + m.modelTarget + " reasoning effort"))
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
	body.WriteString(helpStyle.Render("↑/↓ select · enter save · esc models"))
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
	body.WriteString(helpStyle.Render("enter save · esc models"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}
func (m model) viewContextWindow() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · Gateway model context"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
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

func targetModelSummary(value config.Config, target string) string {
	if target == defaultModelTarget {
		return value.Provider.Model + " · main loop"
	}
	if target == embeddingModelTarget {
		if value.Embedding.Model == "" {
			return "not configured"
		}
		return fmt.Sprintf("%s · %d dimensions", value.Embedding.Model, value.Embedding.Dimensions)
	}
	agent, configured := value.Agents.Roles[target]
	if !configured || agent.Model == "" {
		return "inherits default · " + value.Provider.Model
	}
	summary := agent.Model
	if agent.ReasoningEffort != "" {
		summary += " · effort " + agent.ReasoningEffort
	}
	return summary
}

func (m model) chatHeader() string {
	endpoint := m.config.Provider.BaseURL
	if m.runtime != nil {
		endpoint = m.runtime.Endpoint()
	}
	header := titleStyle.Render("q") + "  " + subtleStyle.Render(m.config.Provider.Model+" · "+endpoint+" · "+m.contextLabel())
	if m.workspaceStore != nil {
		header += "\n" + subtleStyle.Render("workspace · "+filepath.Clean(m.workspaceStore.Root))
	}
	return header
}
func (m model) chatFrameWidth() int {
	return max(36, m.width-4)
}

func (m model) renderedChatHeaderHeight() int {
	rendered := frameStyle.Width(m.chatFrameWidth()).Render(m.chatHeader())
	return lipgloss.Height(rendered) - frameStyle.GetVerticalFrameSize()
}

func (m model) viewChat() string {
	header := m.chatHeader()
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
	return frameStyle.Width(m.chatFrameWidth()).Render(content)
}

func (m model) writeWorkspacePath(body *strings.Builder) {
	if m.workspaceStore != nil {
		body.WriteString(subtleStyle.Render("workspace · " + filepath.Clean(m.workspaceStore.Root)))
		body.WriteString("\n")
	}
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
	return formatTokenCount(int64(tokens))
}

func formatTokenCount(tokens int64) string {
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
