package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/systemoneconfig"
)

func (m *model) initSystemOne(dir string) {
	m.systemOneStore = systemoneconfig.Store{Dir: dir}
	for index, prompt := range []string{"URI", "API key", "Model"} {
		field := textinput.New()
		field.Prompt = ""
		field.Placeholder = prompt
		field.SetWidth(72)
		field.CharLimit = 4096
		if index == 1 {
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
	m.systemOneInputs[0].SetValue(value.URI)
	m.systemOneInputs[1].SetValue(value.APIKey)
	m.systemOneInputs[2].SetValue(value.Model)
	m.systemOneFocus = 0
	m.screen = screenSystemOne
	m.input.Blur()
	m.status = ""
	m.resize(m.width, m.height)
	return m, m.focusSystemOne()
}

func (m *model) focusSystemOne() tea.Cmd {
	for index := range m.systemOneInputs {
		m.systemOneInputs[index].Blur()
	}
	return m.systemOneInputs[m.systemOneFocus].Focus()
}

func (m model) updateSystemOne(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
		if m.systemOneFocus < len(m.systemOneInputs)-1 {
			m.systemOneFocus++
			return m, m.focusSystemOne()
		}
		return m.saveSystemOne()
	case "ctrl+s":
		return m.saveSystemOne()
	}
	var command tea.Cmd
	m.systemOneInputs[m.systemOneFocus], command = m.systemOneInputs[m.systemOneFocus].Update(key)
	return m, command
}

func (m model) saveSystemOne() (tea.Model, tea.Cmd) {
	value := systemoneconfig.Config{
		URI:    strings.TrimSpace(m.systemOneInputs[0].Value()),
		APIKey: m.systemOneInputs[1].Value(),
		Model:  strings.TrimSpace(m.systemOneInputs[2].Value()),
	}
	if err := m.systemOneStore.Save(value); err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.status = "System One settings saved"
	return m, nil
}

func (m model) viewSystemOne() string {
	labels := []string{"Endpoint URI", "API key", "Model"}
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · System One"))
	body.WriteString("\n")
	body.WriteString(subtleStyle.Render("settings · " + m.systemOneStore.Path()))
	body.WriteString("\n\n")
	for index, label := range labels {
		style := subtleStyle
		if index == m.systemOneFocus {
			style = activeLabelStyle
		}
		body.WriteString(style.Render(label))
		body.WriteString("\n")
		body.WriteString(m.systemOneInputs[index].View())
		if index == 1 {
			body.WriteString("\n")
			body.WriteString(subtleStyle.Render("Saved in the settings file; leave blank to use TYPESAFE_API_KEY."))
		}
		body.WriteString("\n\n")
	}
	if m.systemOneInputs[1].Value() == "" && os.Getenv("TYPESAFE_API_KEY") != "" {
		body.WriteString(subtleStyle.Render("API key: using TYPESAFE_API_KEY from the environment"))
		body.WriteString("\n")
	}
	if m.status != "" {
		body.WriteString(subtleStyle.Render(m.status))
		body.WriteString("\n")
	}
	body.WriteString(helpStyle.Render("tab/↑/↓ field · enter next/save · ctrl+s save · esc back"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
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
