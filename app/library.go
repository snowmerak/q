package app

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
)

type librarySettingsSavedMsg struct {
	config qlibrary.Config
	err    error
}

func (m *model) enterLibrarySettings() tea.Cmd {
	value, err := m.librarySettingsStore.LoadOrDefault()
	if err != nil {
		value = qlibrary.DefaultConfig()
	}
	m.librarySettings = value
	m.libraryNetworkFocus = 0
	m.libraryHostInput.SetValue(value.Host)
	m.libraryPortInput.SetValue(strconv.Itoa(value.Port))
	m.screen = screenLibrary
	m.input.Blur()
	m.libraryPortInput.Blur()
	if err != nil {
		m.status = err.Error()
	} else {
		m.status = ""
	}
	return m.libraryHostInput.Focus()
}

func (m model) updateLibrary(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.blurLibraryInputs()
		if m.isStandaloneScreen(screenLibrary) || m.client == nil {
			return m, tea.Quit
		}
		m.screen = screenChat
		m.status = ""
		return m, m.input.Focus()
	case "tab", "down", "up", "shift+tab":
		m.libraryNetworkFocus = 1 - m.libraryNetworkFocus
		command := m.libraryNetworkFocusCommand()
		return m, command
	case "enter":
		host := strings.TrimSpace(m.libraryHostInput.Value())
		if net.ParseIP(host) == nil {
			m.status = "Host must be an IP address"
			command := m.libraryNetworkFocusCommand()
			return m, command
		}
		port, err := strconv.Atoi(strings.TrimSpace(m.libraryPortInput.Value()))
		if err != nil || port < 1 || port > 65535 {
			m.status = "Port must be between 1 and 65535"
			command := m.libraryNetworkFocusCommand()
			return m, command
		}
		value := m.librarySettings
		value.Host = host
		value.Port = port
		if err := value.Validate(); err != nil {
			m.status = err.Error()
			command := m.libraryNetworkFocusCommand()
			return m, command
		}
		store := m.librarySettingsStore
		m.status = "Saving Library network settings…"
		m.blurLibraryInputs()
		return m, func() tea.Msg {
			err := store.Save(value)
			return librarySettingsSavedMsg{config: value, err: err}
		}
	}
	var command tea.Cmd
	if m.libraryNetworkFocus == 0 {
		m.libraryHostInput, command = m.libraryHostInput.Update(key)
	} else {
		m.libraryPortInput, command = m.libraryPortInput.Update(key)
	}
	return m, command
}

func (m model) updateLibraryInput(message tea.Msg) (tea.Model, tea.Cmd) {
	var command tea.Cmd
	if m.libraryNetworkFocus == 0 {
		m.libraryHostInput, command = m.libraryHostInput.Update(message)
	} else {
		m.libraryPortInput, command = m.libraryPortInput.Update(message)
	}
	return m, command
}

func (m *model) libraryNetworkFocusCommand() tea.Cmd {
	if m.libraryNetworkFocus == 0 {
		m.libraryPortInput.Blur()
		return m.libraryHostInput.Focus()
	}
	m.libraryHostInput.Blur()
	return m.libraryPortInput.Focus()
}

func (m *model) blurLibraryInputs() {
	m.libraryHostInput.Blur()
	m.libraryPortInput.Blur()
}

func (m model) viewLibrary() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · Library network"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render(m.librarySettingsStore.Path()))
	body.WriteString("\n")
	body.WriteString(subtleStyle.Render("Saved defaults are used by embedded and foreground Library servers. The port is a fixed rendezvous point."))
	body.WriteString("\n\n" + m.libraryHostInput.View() + "\n\n" + m.libraryPortInput.View() + "\n")
	if m.status != "" {
		body.WriteString("\n" + subtleStyle.Render(m.status) + "\n")
	}
	body.WriteString("\n" + helpStyle.Render("tab/↑/↓ field · enter apply · esc back"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

// RunLibraryConfig opens the Library listener settings without starting chat,
// workspace services, or the Library server itself.
func RunLibraryConfig(ctx context.Context, store config.Store) error {
	m := newModel(ctx, store, nil)
	m.enterLibrarySettings()
	_, err := runStandalone(m, screenLibrary)
	return err
}

func RunLibraryConfigDefault(ctx context.Context) error {
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	if err := RunLibraryConfig(ctx, store); err != nil {
		return fmt.Errorf("q library config: %w", err)
	}
	return nil
}
