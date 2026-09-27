package app

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/systemoneconfig"
)

type systemOneModelsMsg struct {
	requestID uint64
	models    []systemone.Model
	err       error
}

type systemOnePage uint8

const (
	systemOnePageList systemOnePage = iota
	systemOnePageProvider
	systemOnePageNetwork
)

const systemOneProviderRow = 3

const (
	systemOneFieldProviderID = iota
	systemOneFieldURI
	systemOneFieldProviderKey
	systemOneFieldKeyEnv
	systemOneFieldDefaultModel
	systemOneFieldAgentSkillModel
	systemOneFieldHost
	systemOneFieldPort
	systemOneFieldServerKey
)

func (m *model) initSystemOne(dir string) {
	m.systemOneStore = systemoneconfig.Store{Dir: dir}
	for index, prompt := range []string{"Provider ID", "URI", "Provider API key", "API key environment variable", "Default model", "Agent Skill Decision model", "Listen host", "Listen port", "Server API key"} {
		field := textinput.New()
		field.Prompt = ""
		field.Placeholder = prompt
		field.SetWidth(72)
		field.CharLimit = 4096
		if index == systemOneFieldProviderKey || index == systemOneFieldServerKey {
			field.EchoMode = textinput.EchoPassword
		}
		m.systemOneInputs[index] = field
	}
}

func (m model) enterSystemOne() (tea.Model, tea.Cmd) {
	value, err := m.systemOneStore.LoadOrDefault()
	if err != nil {
		m.status = err.Error()
		return m, m.input.Focus()
	}
	m.systemOneConfig = value
	m.systemOneProvider = 0
	if provider, _, err := value.ResolveModel(""); err == nil {
		for index, candidate := range value.Providers {
			if candidate.ID == provider.ID {
				m.systemOneProvider = index
				break
			}
		}
	}
	m.loadSystemOneFields()
	if m.systemOneCancel != nil {
		m.systemOneCancel()
		m.systemOneCancel = nil
	}
	m.systemOneRequestID++
	m.systemOneModels = nil
	m.systemOneLoading = false
	m.systemOnePicking = false
	m.systemOneCursor = 0
	m.systemOnePage = systemOnePageList
	m.systemOneListCursor = systemOneProviderRow + m.systemOneProvider
	m.systemOnePending = false
	m.systemOneFocus = 0
	m.screen = screenSystemOne
	m.input.Blur()
	m.status = ""
	m.resize(m.width, m.height)
	return m, nil
}

func (m *model) loadSystemOneFields() {
	provider := m.systemOneConfig.Providers[m.systemOneProvider]
	for index, value := range []string{
		provider.ID, provider.URI, provider.APIKey, provider.APIKeyEnv,
		m.systemOneConfig.DefaultModel, m.systemOneConfig.RoleModels[systemoneconfig.RoleAgentSkillDecision],
		m.systemOneConfig.Server.Host, strconv.Itoa(m.systemOneConfig.Server.Port), m.systemOneConfig.Server.APIKey,
	} {
		m.systemOneInputs[index].SetValue(value)
	}
}

func (m *model) captureSystemOneFields() {
	oldID := m.systemOneConfig.Providers[m.systemOneProvider].ID
	provider := m.systemOneProviderDraft()
	m.systemOneConfig.Providers[m.systemOneProvider] = provider
	defaultModel := strings.TrimSpace(m.systemOneInputs[systemOneFieldDefaultModel].Value())
	roleModel := strings.TrimSpace(m.systemOneInputs[systemOneFieldAgentSkillModel].Value())
	if oldID != provider.ID {
		replacePrefix := func(model string) string {
			if strings.HasPrefix(model, oldID+"/") {
				return provider.ID + strings.TrimPrefix(model, oldID)
			}
			return model
		}
		defaultModel = replacePrefix(defaultModel)
		roleModel = replacePrefix(roleModel)
		for role, model := range m.systemOneConfig.RoleModels {
			m.systemOneConfig.RoleModels[role] = replacePrefix(model)
		}
		m.systemOneInputs[systemOneFieldDefaultModel].SetValue(defaultModel)
		m.systemOneInputs[systemOneFieldAgentSkillModel].SetValue(roleModel)
	}
	m.systemOneConfig.DefaultModel = defaultModel
	if roleModel != "" {
		if m.systemOneConfig.RoleModels == nil {
			m.systemOneConfig.RoleModels = make(map[string]string)
		}
		m.systemOneConfig.RoleModels[systemoneconfig.RoleAgentSkillDecision] = roleModel
	} else {
		delete(m.systemOneConfig.RoleModels, systemoneconfig.RoleAgentSkillDecision)
	}
	m.systemOneConfig.Server.Host = strings.TrimSpace(m.systemOneInputs[systemOneFieldHost].Value())
	if port, err := strconv.Atoi(strings.TrimSpace(m.systemOneInputs[systemOneFieldPort].Value())); err == nil {
		m.systemOneConfig.Server.Port = port
	} else {
		m.systemOneConfig.Server.Port = -1
	}
	m.systemOneConfig.Server.APIKey = m.systemOneInputs[systemOneFieldServerKey].Value()
}

func (m *model) focusSystemOne() tea.Cmd {
	for index := range m.systemOneInputs {
		m.systemOneInputs[index].Blur()
	}
	if m.systemOnePage == systemOnePageList {
		return nil
	}
	if m.systemOneFocus == systemOneFieldDefaultModel || m.systemOneFocus == systemOneFieldAgentSkillModel ||
		m.systemOneLoading || m.systemOnePicking {
		return nil
	}
	return m.systemOneInputs[m.systemOneFocus].Focus()
}

func (m model) updateSystemOne(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.systemOneLoading || m.systemOnePicking {
		return m.updateSystemOneModels(key)
	}
	if m.systemOnePage == systemOnePageList {
		return m.updateSystemOneList(key)
	}
	return m.updateSystemOneEditor(key)
}

func (m model) updateSystemOneList(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rowCount := systemOneProviderRow + len(m.systemOneConfig.Providers)
	switch key.String() {
	case "esc":
		if m.isStandaloneScreen(screenSystemOne) {
			return m, tea.Quit
		}
		m.screen = screenChat
		m.status = ""
		return m, m.input.Focus()
	case "down", "tab":
		m.systemOneListCursor = (m.systemOneListCursor + 1) % rowCount
		return m, nil
	case "up":
		m.systemOneListCursor = (m.systemOneListCursor - 1 + rowCount) % rowCount
		return m, nil
	case "enter":
		switch m.systemOneListCursor {
		case 0:
			m.systemOneFocus = systemOneFieldDefaultModel
			return m.loadSystemOneModels()
		case 1:
			m.systemOneFocus = systemOneFieldAgentSkillModel
			return m.loadSystemOneModels()
		case 2:
			m.systemOnePage = systemOnePageNetwork
			m.systemOneFocus = systemOneFieldHost
			m.status = ""
			return m, m.focusSystemOne()
		default:
			m.systemOneProvider = m.systemOneListCursor - systemOneProviderRow
			m.loadSystemOneFields()
			m.systemOnePage = systemOnePageProvider
			m.systemOneFocus = systemOneFieldProviderID
			m.status = ""
			return m, m.focusSystemOne()
		}
	case "a":
		id := fmt.Sprintf("provider-%d", len(m.systemOneConfig.Providers)+1)
		for m.systemOneIDExists(id) {
			id += "-new"
		}
		candidate := m.systemOneConfig
		candidate.Providers = append(append([]systemoneconfig.ProviderConfig(nil), candidate.Providers...), systemoneconfig.ProviderConfig{
			ID: id, URI: systemoneconfig.DefaultURI,
		})
		if err := m.systemOneStore.Save(candidate); err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.systemOneConfig = candidate
		m.systemOneProvider = len(m.systemOneConfig.Providers) - 1
		m.systemOneListCursor = systemOneProviderRow + m.systemOneProvider
		m.loadSystemOneFields()
		m.systemOnePage = systemOnePageProvider
		m.systemOneFocus = systemOneFieldProviderID
		m.status = "Provider added"
		return m, m.focusSystemOne()
	case "d":
		if m.systemOneListCursor < systemOneProviderRow {
			return m, nil
		}
		if len(m.systemOneConfig.Providers) == 1 {
			m.status = "At least one provider is required"
			return m, nil
		}
		index := m.systemOneListCursor - systemOneProviderRow
		candidate := m.systemOneConfig
		candidate.Providers = append(
			append([]systemoneconfig.ProviderConfig(nil), candidate.Providers[:index]...),
			candidate.Providers[index+1:]...,
		)
		if err := m.systemOneStore.Save(candidate); err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.systemOneConfig = candidate
		m.systemOneProvider = min(index, len(candidate.Providers)-1)
		m.systemOneListCursor = systemOneProviderRow + m.systemOneProvider
		m.loadSystemOneFields()
		m.status = "Provider removed"
		return m, nil
	}
	return m, nil
}

func (m model) updateSystemOneEditor(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	first, last := systemOneFieldProviderID, systemOneFieldKeyEnv
	if m.systemOnePage == systemOnePageNetwork {
		first, last = systemOneFieldHost, systemOneFieldServerKey
	}
	switch key.String() {
	case "esc":
		value, err := m.systemOneStore.LoadOrDefault()
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.systemOneConfig = value
		m.systemOnePending = false
		m.systemOneProvider = min(m.systemOneProvider, len(value.Providers)-1)
		m.loadSystemOneFields()
		m.systemOnePage = systemOnePageList
		m.status = ""
		return m, m.focusSystemOne()
	case "enter":
		if m.systemOneFocus == last {
			if m.systemOnePending {
				return m, nil
			}
			m.systemOnePage = systemOnePageList
			return m, m.focusSystemOne()
		}
		m.systemOneFocus++
		return m, m.focusSystemOne()
	case "tab", "down":
		m.systemOneFocus++
		if m.systemOneFocus > last {
			m.systemOneFocus = first
		}
		return m, m.focusSystemOne()
	case "shift+tab", "up":
		m.systemOneFocus--
		if m.systemOneFocus < first {
			m.systemOneFocus = last
		}
		return m, m.focusSystemOne()
	}
	return m.updateSystemOneInput(key)
}

func (m model) updateSystemOneInput(message tea.Msg) (tea.Model, tea.Cmd) {
	if m.systemOneLoading || m.systemOnePicking || m.systemOnePage == systemOnePageList {
		return m, nil
	}
	first, last := systemOneFieldProviderID, systemOneFieldKeyEnv
	if m.systemOnePage == systemOnePageNetwork {
		first, last = systemOneFieldHost, systemOneFieldServerKey
	}
	if m.systemOneFocus < first || m.systemOneFocus > last {
		return m, nil
	}
	before := m.systemOneInputs[m.systemOneFocus].Value()
	var command tea.Cmd
	m.systemOneInputs[m.systemOneFocus], command = m.systemOneInputs[m.systemOneFocus].Update(message)
	if m.systemOneInputs[m.systemOneFocus].Value() != before {
		m.persistSystemOne()
	}
	return m, command
}

func (m model) systemOneProviderDraft() systemoneconfig.ProviderConfig {
	return systemoneconfig.ProviderConfig{
		ID:        strings.TrimSpace(m.systemOneInputs[systemOneFieldProviderID].Value()),
		URI:       strings.TrimSpace(m.systemOneInputs[systemOneFieldURI].Value()),
		APIKey:    m.systemOneInputs[systemOneFieldProviderKey].Value(),
		APIKeyEnv: strings.TrimSpace(m.systemOneInputs[systemOneFieldKeyEnv].Value()),
	}
}

func (m model) systemOneIDExists(id string) bool {
	for _, provider := range m.systemOneConfig.Providers {
		if provider.ID == id {
			return true
		}
	}
	return false
}

func (m model) loadSystemOneModels() (tea.Model, tea.Cmd) {
	providers := append([]systemoneconfig.ProviderConfig(nil), m.systemOneConfig.Providers...)
	ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
	m.systemOneCancel = cancel
	m.systemOneRequestID++
	requestID := m.systemOneRequestID
	m.systemOneModelTarget = m.systemOneFocus
	m.systemOneLoading = true
	m.systemOneModels = nil
	m.status = "Loading models from System One providers…"
	m.focusSystemOne()
	return m, func() tea.Msg {
		defer cancel()
		models := make([]systemone.Model, 0)
		var firstError error
		for _, provider := range providers {
			client, err := provider.NewClient()
			if err == nil {
				var result *systemone.ModelsResult
				result, err = client.ListModels(ctx)
				if err == nil {
					for _, candidate := range result.Models {
						candidate.Name = provider.ID + "/" + candidate.Name
						models = append(models, candidate)
					}
				}
			}
			if err != nil && firstError == nil {
				firstError = fmt.Errorf("%s: %w", provider.ID, err)
			}
		}
		if len(models) == 0 && firstError != nil {
			return systemOneModelsMsg{requestID: requestID, err: firstError}
		}
		return systemOneModelsMsg{requestID: requestID, models: models}
	}
}

func (m model) receiveSystemOneModels(message systemOneModelsMsg) (tea.Model, tea.Cmd) {
	if m.screen != screenSystemOne || !m.systemOneLoading || message.requestID != m.systemOneRequestID {
		return m, nil
	}
	m.systemOneLoading = false
	m.systemOneCancel = nil
	if message.err != nil {
		m.status = "Load System One models: " + message.err.Error()
		return m, m.focusSystemOne()
	}
	if len(message.models) == 0 {
		m.status = "No models returned by the configured System One providers"
		return m, m.focusSystemOne()
	}
	if m.systemOneModelTarget == systemOneFieldAgentSkillModel {
		message.models = append([]systemone.Model{{Description: "Use default model"}}, message.models...)
	}
	m.systemOneModels = message.models
	m.systemOneCursor = 0
	for index, candidate := range message.models {
		if candidate.Name == m.systemOneInputs[m.systemOneModelTarget].Value() {
			m.systemOneCursor = index
			break
		}
	}
	m.systemOnePicking = true
	m.status = ""
	return m, nil
}

func (m model) updateSystemOneModels(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.systemOneLoading {
		if key.String() == "esc" {
			if m.systemOneCancel != nil {
				m.systemOneCancel()
				m.systemOneCancel = nil
			}
			m.systemOneRequestID++
			m.systemOneLoading = false
			m.status = ""
			return m, m.focusSystemOne()
		}
		return m, nil
	}
	switch key.String() {
	case "esc":
		m.systemOnePicking = false
		m.status = ""
		return m, m.focusSystemOne()
	case "up", "k":
		m.systemOneCursor = (m.systemOneCursor - 1 + len(m.systemOneModels)) % len(m.systemOneModels)
	case "down", "j":
		m.systemOneCursor = (m.systemOneCursor + 1) % len(m.systemOneModels)
	case "enter":
		selected := m.systemOneModels[m.systemOneCursor].Name
		m.systemOneInputs[m.systemOneModelTarget].SetValue(selected)
		m.systemOnePicking = false
		if m.persistSystemOne() {
			if selected == "" {
				m.status = "Agent Skill Decision uses the default model"
			} else {
				m.status = "Selected " + selected
			}
		}
		return m, nil
	}
	return m, nil
}

func (m *model) persistSystemOne() bool {
	m.captureSystemOneFields()
	if err := m.systemOneStore.Save(m.systemOneConfig); err != nil {
		m.systemOnePending = true
		m.status = err.Error()
		return false
	}
	m.systemOnePending = false
	m.status = "System One settings saved"
	return true
}

func (m model) viewSystemOne() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · System One"))
	body.WriteString("\n")
	body.WriteString(subtleStyle.Render("settings · " + m.systemOneStore.Path()))
	body.WriteString("\n\n")
	if m.systemOnePicking {
		body.WriteString(m.viewSystemOneModels())
	} else if m.systemOneLoading {
		body.WriteString(subtleStyle.Render("Loading models from configured providers…"))
	} else if m.systemOnePage == systemOnePageList {
		body.WriteString(m.viewSystemOneList())
	} else {
		body.WriteString(m.viewSystemOneEditor())
	}
	if m.status != "" {
		body.WriteString("\n")
		body.WriteString(subtleStyle.Render(m.status))
		body.WriteString("\n")
	}
	help := "↑/↓/tab select · enter open · a add provider · d delete provider · esc back"
	if m.systemOnePage != systemOnePageList {
		help = "tab/↑/↓ field · enter next/back · esc back"
	}
	if m.systemOnePicking {
		help = "↑/↓ select · enter choose and save · esc back"
	} else if m.systemOneLoading {
		help = "esc cancel"
	}
	body.WriteString("\n")
	body.WriteString(helpStyle.Render(help))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewSystemOneList() string {
	var body strings.Builder
	rows := []string{
		"Default model · " + m.systemOneConfig.DefaultModel,
		"Agent Skill Decision · " + m.systemOneConfig.ModelForRole(systemoneconfig.RoleAgentSkillDecision),
		"Network · " + m.systemOneConfig.Server.Host + ":" + strconv.Itoa(m.systemOneConfig.Server.Port),
	}
	for _, provider := range m.systemOneConfig.Providers {
		rows = append(rows, "Provider · "+provider.ID+" · "+provider.URI)
	}
	for index, row := range rows {
		prefix := "  "
		style := subtleStyle
		if index == m.systemOneListCursor {
			prefix = "› "
			style = activeLabelStyle
		}
		body.WriteString(style.Render(prefix + row))
		body.WriteString("\n")
	}
	return body.String()
}

func (m model) viewSystemOneEditor() string {
	var body strings.Builder
	first, last := systemOneFieldProviderID, systemOneFieldKeyEnv
	labels := []string{"Provider ID", "Endpoint URI", "Provider API key", "API key env"}
	if m.systemOnePage == systemOnePageNetwork {
		first, last = systemOneFieldHost, systemOneFieldServerKey
		labels = []string{"Listen host", "Listen port", "Server API key"}
		body.WriteString(agentTraceTitleStyle(m.dark).Render("NETWORK"))
	} else {
		body.WriteString(agentTraceTitleStyle(m.dark).Render(fmt.Sprintf(
			"PROVIDER %d/%d", m.systemOneProvider+1, len(m.systemOneConfig.Providers),
		)))
	}
	body.WriteString("\n\n")
	for index := first; index <= last; index++ {
		label := labels[index-first]
		m.systemOneInputs[index].SetWidth(max(16, min(72, m.width-28)))
		style := subtleStyle
		if index == m.systemOneFocus {
			style = activeLabelStyle
		}
		body.WriteString(style.Render(fmt.Sprintf("%-18s", label)))
		body.WriteString(m.systemOneInputs[index].View())
		body.WriteString("\n")
	}
	if m.systemOnePage == systemOnePageNetwork {
		body.WriteString("\n")
		body.WriteString(subtleStyle.Render("A public listen host requires a server API key."))
	} else if m.systemOneInputs[systemOneFieldProviderKey].Value() == "" &&
		os.Getenv(m.systemOneInputs[systemOneFieldKeyEnv].Value()) != "" {
		body.WriteString("\n")
		body.WriteString(subtleStyle.Render("Using " + m.systemOneInputs[systemOneFieldKeyEnv].Value() + " from the environment"))
	}
	return body.String()
}

func (m model) viewSystemOneModels() string {
	var body strings.Builder
	body.WriteString(agentTraceTitleStyle(m.dark).Render("AVAILABLE MODELS"))
	body.WriteString("\n")
	start, end := lspVisibleRange(len(m.systemOneModels), m.systemOneCursor, max(3, m.height-10))
	for index := start; index < end; index++ {
		candidate := m.systemOneModels[index]
		prefix := "  "
		style := subtleStyle
		if index == m.systemOneCursor {
			prefix = "› "
			style = activeLabelStyle
		}
		line := prefix + candidate.Name
		if candidate.Name == "" {
			line += candidate.Description
		}
		if candidate.ReleaseDate != "" {
			line += " · " + candidate.ReleaseDate
		}
		body.WriteString(style.Render(line))
		body.WriteString("\n")
	}
	return body.String()
}

// RunSystemOne opens System One settings without starting chat or Gateway.
func RunSystemOne(ctx context.Context, store config.Store) error {
	m := newModel(ctx, store, nil)
	updated, _ := m.enterSystemOne()
	m = updated.(model)
	if m.screen != screenSystemOne {
		return fmt.Errorf("q systemone: %s", m.status)
	}
	_, err := runStandalone(m, screenSystemOne)
	return err
}

func RunSystemOneDefault(ctx context.Context) error {
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	return RunSystemOne(ctx, store)
}
