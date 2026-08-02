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

	config         config.Config
	client         chatClient
	messages       []client.Message
	conversationID string
	input          textarea.Model
	viewport       viewport.Model
	spinner        spinner.Model
	waiting        bool
}

type configuredMsg struct {
	config config.Config
	client chatClient
	err    error
}

type chatResultMsg struct {
	response *client.ChatResponse
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
		m.enterChat(message.config, message.client)
		return m, m.input.Focus()
	case modelsResultMsg:
		m.discovering = false
		if message.err != nil {
			m.status = message.err.Error()
			return m, m.setup[m.setupFocus].Focus()
		}
		m.enterModelPicker(message.config, message.models)
		if m.screen == screenSetup {
			return m, m.setup[m.setupFocus].Focus()
		}
		return m, m.modelFilter.Focus()
	case chatResultMsg:
		m.waiting = false
		if message.err != nil {
			m.status = message.err.Error()
			return m, m.input.Focus()
		}
		if message.response == nil || len(message.response.Choices) == 0 {
			m.status = "provider returned no choices"
			return m, m.input.Focus()
		}
		assistant := message.response.Choices[0].Message
		if assistant.Role == "" {
			assistant.Role = client.RoleAssistant
		}
		m.messages = append(m.messages, assistant)
		if message.response.ConversationID != "" {
			m.conversationID = message.response.ConversationID
		}
		m.status = ""
		m.refreshTranscript()
		return m, m.input.Focus()
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

func (m model) updateModelPicker(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	filtered := m.filteredModels()
	switch key.String() {
	case "esc":
		m.screen = screenSetup
		m.status = ""
		m.modelFilter.Blur()
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
		value.Provider.Model = filtered[m.modelCursor].ID
		return m.saveConfiguration(value)
	}
	var command tea.Cmd
	m.modelFilter, command = m.modelFilter.Update(key)
	m.modelCursor = 0
	return m, command
}

func (m model) saveConfiguration(value config.Config) (tea.Model, tea.Cmd) {
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
		return configuredMsg{config: value, client: configuredClient, err: err}
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
	m.messages = append(m.messages, client.Message{Role: client.RoleUser, Content: content})
	m.input.Reset()
	m.input.Blur()
	m.waiting = true
	m.status = "Thinking…"
	m.refreshTranscript()

	history := append([]client.Message(nil), m.messages...)
	conversationID := m.conversationID
	configuredClient := m.client
	request := func() tea.Msg {
		response, err := configuredClient.Chat(m.ctx, client.ChatRequest{
			Messages: history, ConversationID: conversationID,
		})
		return chatResultMsg{response: response, err: err}
	}
	return m, tea.Batch(m.spinner.Tick, request)
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
	m.conversationID = ""
	m.waiting = false
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
		m.screen = screenSetup
		m.status = "provider returned no models"
		return
	}
	m.screen = screenModels
	m.draftConfig = value
	m.modelCursor = 0
	m.modelFilter.Reset()
	m.status = ""
	if m.setupEdit {
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
	m.conversationID = ""
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
		body.WriteString(label + "\n" + field.View() + "\n\n")
	}
	if m.status != "" {
		statusStyle := errorStyle
		if m.discovering {
			statusStyle = subtleStyle
		}
		body.WriteString(statusStyle.Render(m.status) + "\n")
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
	if len(filtered) == 0 {
		body.WriteString(emptyStyle.Render("No matching models"))
	} else {
		for index := start; index < end; index++ {
			prefix := "  "
			style := subtleStyle
			if index == m.modelCursor {
				prefix = "› "
				style = activeLabelStyle
			}
			body.WriteString(prefix + style.Render(filtered[index].ID) + "\n")
		}
	}
	body.WriteString("\n")
	body.WriteString(helpStyle.Render(fmt.Sprintf("%d/%d models · type to filter · ↑/↓ select · enter save · esc back", len(filtered), len(m.models))))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewChat() string {
	header := titleStyle.Render("q") + "  " + subtleStyle.Render(m.config.Provider.Model+" · "+m.config.Provider.BaseURL)
	status := m.status
	if m.waiting {
		status = m.spinner.View() + " " + status
	}
	footer := helpStyle.Render("ctrl+s send · enter newline · ctrl+l clear · ctrl+p provider · esc quit")
	content := header + "\n\n" + m.viewport.View() + "\n" + m.input.View()
	if status != "" {
		content += "\n" + subtleStyle.Render(status)
	}
	content += "\n" + footer
	return frameStyle.Width(max(36, m.width-4)).Render(content)
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
