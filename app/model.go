package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/memory"
)

type screen uint8

const (
	screenSetup screen = iota
	screenModels
	screenChat
)

const (
	setupBaseURL = iota
	setupAPIKeyEnv
	setupAPIKey
	setupFieldCount
)

type model struct {
	ctx     context.Context
	store   config.Store
	factory clientFactory
	screen  screen
	width   int
	height  int
	status  string

	setup       [setupFieldCount]textinput.Model
	setupFocus  int
	setupEdit   bool
	discovering bool
	models      []client.Model
	modelCursor int
	modelFilter textinput.Model
	draftConfig config.Config
	modelReturn screen

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

type modelsResultMsg struct {
	models []client.Model
	config config.Config
	err    error
}

func newModel(ctx context.Context, store config.Store, factory clientFactory) model {
	defaults := config.Default()
	m := model{
		ctx: ctx, store: store, factory: factory, screen: screenSetup,
		viewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(12)),
		spinner:  spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
	m.viewport.SoftWrap = true

	placeholders := []string{
		"https://api.openai.com/v1",
		"OPENAI_API_KEY",
		"optional inline key",
	}
	values := []string{
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
	m.setup[m.setupFocus].Focus()
	m.modelFilter = textinput.New()
	m.modelFilter.Prompt = "/ "
	m.modelFilter.Placeholder = "Filter models"
	m.modelFilter.SetWidth(60)
	m.input = newChatInput()
	return m
}

func newChatInput() textarea.Model {
	input := textarea.New()
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
	return tea.Batch(m.setup[m.setupFocus].Focus(), tea.RequestBackgroundColor)
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
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
	}

	if key, ok := message.(tea.KeyPressMsg); ok {
		if key.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.screen == screenSetup {
			return m.updateSetup(key)
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
		if m.setupEdit && m.client != nil {
			m.screen = screenChat
			m.status = ""
			return m, m.input.Focus()
		}
		return m, tea.Quit
	case "tab", "down", "enter":
		if key.String() == "enter" && m.setupFocus == setupFieldCount-1 {
			return m.discoverModels()
		}
		return m.moveSetupFocus(1)
	case "shift+tab", "up":
		return m.moveSetupFocus(-1)
	}
	var command tea.Cmd
	m.setup[m.setupFocus], command = m.setup[m.setupFocus].Update(key)
	return m, command
}

func (m model) moveSetupFocus(delta int) (tea.Model, tea.Cmd) {
	m.setup[m.setupFocus].Blur()
	m.setupFocus = (m.setupFocus + delta + setupFieldCount) % setupFieldCount
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
			m.enterSetup(m.config)
			return m, m.setup[m.setupFocus].Focus()
		}
		return m, nil
	case "ctrl+s":
		return m.submitChat()
	case "enter":
		return m.submitChat()
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

func (m model) submitChat() (tea.Model, tea.Cmd) {
	content := strings.TrimSpace(m.input.Value())
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
	m.input.SetStyles(textarea.DefaultStyles(dark))
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
	if m.screen == screenModels {
		content = m.viewModels()
	} else if m.screen == screenChat {
		content = m.viewChat()
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "q"
	return view
}

func (m model) viewSetup() string {
	labels := []string{"Base URL", "API key environment variable", "API key (optional, stored in config)"}
	var body strings.Builder
	title := "q · first-run setup"
	if m.setupEdit {
		title = "q · provider settings"
	}
	body.WriteString(titleStyle.Render(title))
	body.WriteString("\n")
	body.WriteString(subtleStyle.Render("OpenAI-compatible provider · " + m.store.Path()))
	body.WriteString("\n\n")
	for index, field := range m.setup {
		label := subtleStyle.Render(labels[index])
		if index == m.setupFocus {
			label = activeLabelStyle.Render(labels[index])
		}
		body.WriteString(label)
		body.WriteString("\n")
		body.WriteString(field.View())
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
	help := "tab/↑/↓ navigate · enter next/save · esc quit"
	if m.setupEdit {
		help = "tab/↑/↓ navigate · enter next/save · esc cancel"
	}
	body.WriteString(helpStyle.Render(help))
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
	body.WriteString(subtleStyle.Render(m.draftConfig.Provider.BaseURL))
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
	header := titleStyle.Render("q") + "  " + subtleStyle.Render(m.config.Provider.Model+" · "+m.config.Provider.BaseURL+" · "+m.contextLabel())
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
