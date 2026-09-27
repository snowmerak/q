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

func (m *model) initSystemOne(dir string) {
	m.systemOneStore = systemoneconfig.Store{Dir: dir}
	for index, prompt := range []string{"URI", "API key", "Model", "Provider ID", "API key environment variable", "Listen host", "Listen port", "Server API key"} {
		field := textinput.New()
		field.Prompt = ""
		field.Placeholder = prompt
		field.SetWidth(72)
		field.CharLimit = 4096
		if index == 1 || index == 7 {
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
	for index, provider := range value.Providers {
		if provider.ID == value.Selected {
			m.systemOneProvider = index
			break
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
	m.systemOneFocus = 0
	m.screen = screenSystemOne
	m.input.Blur()
	m.status = ""
	m.resize(m.width, m.height)
	return m, m.focusSystemOne()
}

func (m *model) loadSystemOneFields() {
	provider := m.systemOneConfig.Providers[m.systemOneProvider]
	for index, value := range []string{
		provider.URI, provider.APIKey, provider.Model, provider.ID, provider.APIKeyEnv,
		m.systemOneConfig.Server.Host, strconv.Itoa(m.systemOneConfig.Server.Port), m.systemOneConfig.Server.APIKey,
	} {
		m.systemOneInputs[index].SetValue(value)
	}
}

func (m *model) captureSystemOneFields() {
	oldID := m.systemOneConfig.Providers[m.systemOneProvider].ID
	provider := m.systemOneProviderDraft()
	m.systemOneConfig.Providers[m.systemOneProvider] = provider
	if m.systemOneConfig.Selected == oldID {
		m.systemOneConfig.Selected = provider.ID
	}
	m.systemOneConfig.Server.Host = strings.TrimSpace(m.systemOneInputs[5].Value())
	if port, err := strconv.Atoi(strings.TrimSpace(m.systemOneInputs[6].Value())); err == nil {
		m.systemOneConfig.Server.Port = port
	} else {
		m.systemOneConfig.Server.Port = -1
	}
	m.systemOneConfig.Server.APIKey = m.systemOneInputs[7].Value()
}

func (m *model) focusSystemOne() tea.Cmd {
	for index := range m.systemOneInputs {
		m.systemOneInputs[index].Blur()
	}
	if m.systemOneFocus == 2 || m.systemOneLoading || m.systemOnePicking {
		return nil
	}
	return m.systemOneInputs[m.systemOneFocus].Focus()
}

func (m model) updateSystemOne(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.systemOneLoading || m.systemOnePicking {
		return m.updateSystemOneModels(key)
	}
	switch key.String() {
	case "esc":
		for index := range m.systemOneInputs {
			m.systemOneInputs[index].Blur()
		}
		if m.isStandaloneScreen(screenSystemOne) {
			return m, tea.Quit
		}
		m.screen = screenChat
		m.status = ""
		return m, m.input.Focus()
	case "tab", "down":
		m.systemOneFocus = (m.systemOneFocus + 1) % len(m.systemOneInputs)
		return m, m.focusSystemOne()
	case "shift+tab", "up":
		m.systemOneFocus = (m.systemOneFocus - 1 + len(m.systemOneInputs)) % len(m.systemOneInputs)
		return m, m.focusSystemOne()
	case "enter":
		if m.systemOneFocus != 2 {
			if m.systemOneFocus == len(m.systemOneInputs)-1 {
				m.systemOneFocus = 0
			} else {
				m.systemOneFocus++
			}
			return m, m.focusSystemOne()
		}
		return m.loadSystemOneModels()
	case "ctrl+n":
		m.captureSystemOneFields()
		id := fmt.Sprintf("provider-%d", len(m.systemOneConfig.Providers)+1)
		for m.systemOneIDExists(id) {
			id += "-new"
		}
		m.systemOneConfig.Providers = append(m.systemOneConfig.Providers, systemoneconfig.ProviderConfig{
			ID: id, URI: systemoneconfig.DefaultURI, Model: systemoneconfig.DefaultModel,
		})
		m.systemOneProvider = len(m.systemOneConfig.Providers) - 1
		m.systemOneConfig.Selected = id
		m.loadSystemOneFields()
		m.systemOneFocus = 3
		m.status = "Provider added · configure its API key and press ctrl+s to save"
		return m, m.focusSystemOne()
	case "ctrl+p":
		m.captureSystemOneFields()
		m.systemOneProvider = (m.systemOneProvider + 1) % len(m.systemOneConfig.Providers)
		m.systemOneConfig.Selected = m.systemOneConfig.Providers[m.systemOneProvider].ID
		m.loadSystemOneFields()
		m.status = ""
		return m, m.focusSystemOne()
	case "ctrl+d":
		if len(m.systemOneConfig.Providers) == 1 {
			m.status = "At least one provider is required"
			return m, nil
		}
		m.captureSystemOneFields()
		m.systemOneConfig.Providers = append(
			m.systemOneConfig.Providers[:m.systemOneProvider],
			m.systemOneConfig.Providers[m.systemOneProvider+1:]...,
		)
		if m.systemOneProvider >= len(m.systemOneConfig.Providers) {
			m.systemOneProvider = len(m.systemOneConfig.Providers) - 1
		}
		m.systemOneConfig.Selected = m.systemOneConfig.Providers[m.systemOneProvider].ID
		m.loadSystemOneFields()
		m.status = "Provider removed · press ctrl+s to save"
		return m, m.focusSystemOne()
	case "ctrl+s":
		return m.saveSystemOne()
	}
	if m.systemOneFocus == 2 {
		return m, nil
	}
	var command tea.Cmd
	m.systemOneInputs[m.systemOneFocus], command = m.systemOneInputs[m.systemOneFocus].Update(key)
	return m, command
}

func (m model) systemOneProviderDraft() systemoneconfig.ProviderConfig {
	return systemoneconfig.ProviderConfig{
		ID:        strings.TrimSpace(m.systemOneInputs[3].Value()),
		URI:       strings.TrimSpace(m.systemOneInputs[0].Value()),
		APIKey:    m.systemOneInputs[1].Value(),
		APIKeyEnv: strings.TrimSpace(m.systemOneInputs[4].Value()),
		Model:     strings.TrimSpace(m.systemOneInputs[2].Value()),
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
	client, err := m.systemOneProviderDraft().NewClient()
	if err != nil {
		m.status = err.Error()
		return m, nil
	}
	ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
	m.systemOneCancel = cancel
	m.systemOneRequestID++
	requestID := m.systemOneRequestID
	m.systemOneLoading = true
	m.systemOneModels = nil
	m.status = "Loading System One models…"
	m.focusSystemOne()
	return m, func() tea.Msg {
		defer cancel()
		result, err := client.ListModels(ctx)
		if err != nil {
			return systemOneModelsMsg{requestID: requestID, err: err}
		}
		return systemOneModelsMsg{requestID: requestID, models: result.Models}
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
		m.status = "No models returned by the System One endpoint"
		return m, m.focusSystemOne()
	}
	m.systemOneModels = message.models
	m.systemOneCursor = 0
	for index, candidate := range message.models {
		if candidate.Name == m.systemOneInputs[2].Value() {
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
		m.systemOneInputs[2].SetValue(selected)
		m.systemOnePicking = false
		m.status = "Selected " + selected + " · press ctrl+s to save"
		return m, m.focusSystemOne()
	}
	return m, nil
}

func (m model) saveSystemOne() (tea.Model, tea.Cmd) {
	m.captureSystemOneFields()
	if err := m.systemOneStore.Save(m.systemOneConfig); err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.status = "System One settings saved"
	return m, nil
}

func (m model) viewSystemOne() string {
	labels := []string{"Endpoint URI", "Provider API key", "Model", "Provider ID", "API key env", "Listen host", "Listen port", "Server API key"}
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · System One"))
	body.WriteString("\n")
	body.WriteString(subtleStyle.Render("settings · " + m.systemOneStore.Path()))
	body.WriteString("\n")
	body.WriteString(activeLabelStyle.Render(fmt.Sprintf("Provider %d/%d · %s", m.systemOneProvider+1, len(m.systemOneConfig.Providers), m.systemOneInputs[3].Value())))
	body.WriteString("\n\n")
	if m.systemOnePicking {
		body.WriteString(m.viewSystemOneModels())
	} else if m.systemOneLoading {
		body.WriteString(subtleStyle.Render("Loading available models from the configured endpoint…"))
	} else {
		for index, label := range labels {
			m.systemOneInputs[index].SetWidth(max(16, min(72, m.width-28)))
			style := subtleStyle
			if index == m.systemOneFocus {
				style = activeLabelStyle
			}
			body.WriteString(style.Render(fmt.Sprintf("%-18s", label)))
			if index == 2 {
				body.WriteString(activeLabelStyle.Render("‹ " + m.systemOneInputs[index].Value() + " ›"))
			} else {
				body.WriteString(m.systemOneInputs[index].View())
			}
			body.WriteString("\n")
		}
		body.WriteString("\n")
		body.WriteString(subtleStyle.Render("Enter on Model loads the provider's model list. A public listen host requires a server API key."))
		body.WriteString("\n")
	}
	if m.systemOneInputs[1].Value() == "" && os.Getenv(m.systemOneInputs[4].Value()) != "" {
		body.WriteString(subtleStyle.Render("Provider API key: using " + m.systemOneInputs[4].Value() + " from the environment"))
		body.WriteString("\n")
	}
	if m.status != "" {
		body.WriteString(subtleStyle.Render(m.status))
		body.WriteString("\n")
	}
	help := "tab/↑/↓ field · enter next/list models · ctrl+n add · ctrl+p next provider · ctrl+d remove · ctrl+s save · esc back"
	if m.systemOnePicking {
		help = "↑/↓ select · enter choose · esc back"
	} else if m.systemOneLoading {
		help = "esc cancel"
	}
	body.WriteString(helpStyle.Render(help))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
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
