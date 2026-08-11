package app

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/gatewayconfig"
)

const (
	gatewaySectionNetwork = iota
	gatewaySectionKeys
	gatewaySectionProviders
	gatewaySectionCount
)

type gatewaySettingsSavedMsg struct {
	config gatewayconfig.Config
	err    error
}

type gatewayAPIKeyGeneratedMsg struct {
	config gatewayconfig.Config
	key    string
	err    error
}

type gatewayAPIKeyRevokedMsg struct {
	config gatewayconfig.Config
	err    error
}

func (m *model) enterGatewaySettings() {
	value, err := m.gatewaySettingsStore.LoadOrDefault()
	m.gatewaySettings = value
	if m.runtime != nil {
		m.gatewayConfig = m.runtime.Config()
	}
	m.screen = screenGateway
	m.gatewayKeyAdding = false
	m.gatewayKeyRevokeArmed = false
	m.generatedGatewayKey = ""
	m.blurGatewayInputs()
	m.input.Blur()
	if err != nil {
		m.status = err.Error()
	} else {
		m.status = ""
	}
}

func (m *model) enterGatewayNetwork() tea.Cmd {
	m.screen = screenGatewayNetwork
	m.gatewayNetworkFocus = 0
	m.gatewayHostInput.SetValue(m.gatewaySettings.Server.Host)
	m.gatewayPortInput.SetValue(strconv.Itoa(m.gatewaySettings.Server.Port))
	m.status = ""
	m.gatewayPortInput.Blur()
	return m.gatewayHostInput.Focus()
}

func (m *model) enterGatewayKeys() tea.Cmd {
	m.screen = screenGatewayKeys
	m.gatewayKeyAdding = false
	m.gatewayKeyRevokeArmed = false
	m.generatedGatewayKey = ""
	m.gatewayKeyAlias.Blur()
	if m.gatewayKeyCursor >= len(m.gatewaySettings.APIKeys) {
		m.gatewayKeyCursor = max(0, len(m.gatewaySettings.APIKeys)-1)
	}
	m.status = ""
	return nil
}

func (m model) updateGateway(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		if m.isStandaloneScreen(screenGateway) || m.gatewayConfigOnly || m.client == nil {
			return m, tea.Quit
		}
		m.screen = screenChat
		m.status = ""
		return m, m.input.Focus()
	case "up":
		m.gatewayCursor = (m.gatewayCursor - 1 + gatewaySectionCount) % gatewaySectionCount
		return m, nil
	case "down", "tab":
		m.gatewayCursor = (m.gatewayCursor + 1) % gatewaySectionCount
		return m, nil
	case "enter":
		switch m.gatewayCursor {
		case gatewaySectionNetwork:
			return m, m.enterGatewayNetwork()
		case gatewaySectionKeys:
			return m, m.enterGatewayKeys()
		case gatewaySectionProviders:
			m.providerReturn = screenGateway
			m.enterProviderList()
			return m, nil
		}
	}
	return m, nil
}

func (m model) updateGatewayNetwork(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.enterGatewaySettings()
		return m, nil
	case "tab", "down", "up", "shift+tab":
		m.gatewayNetworkFocus = 1 - m.gatewayNetworkFocus
		return m, m.gatewayNetworkFocusCommand()
	case "ctrl+s", "enter":
		host := strings.TrimSpace(m.gatewayHostInput.Value())
		if net.ParseIP(host) == nil {
			m.status = "Host must be an IP address"
			return m, m.gatewayNetworkFocusCommand()
		}
		port, err := strconv.Atoi(strings.TrimSpace(m.gatewayPortInput.Value()))
		if err != nil || port < 0 || port > 65535 {
			m.status = "Port must be between 0 and 65535"
			return m, m.gatewayNetworkFocusCommand()
		}
		value := m.gatewaySettings
		value.Server = gatewayconfig.ServerConfig{Host: host, Port: port}
		store := m.gatewaySettingsStore
		m.status = "Saving Gateway network settings…"
		m.blurGatewayInputs()
		return m, func() tea.Msg {
			err := store.Save(value)
			return gatewaySettingsSavedMsg{config: value, err: err}
		}
	}
	var command tea.Cmd
	if m.gatewayNetworkFocus == 0 {
		m.gatewayHostInput, command = m.gatewayHostInput.Update(key)
	} else {
		m.gatewayPortInput, command = m.gatewayPortInput.Update(key)
	}
	return m, command
}

func (m model) updateGatewayKeys(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.generatedGatewayKey != "" {
		switch key.String() {
		case "enter", "esc":
			m.generatedGatewayKey = ""
			m.status = "API key generated"
		}
		return m, nil
	}
	if m.gatewayKeyAdding {
		switch key.String() {
		case "esc":
			m.gatewayKeyAdding = false
			m.gatewayKeyAlias.Reset()
			m.gatewayKeyAlias.Blur()
			m.status = ""
			return m, nil
		case "enter":
			alias := strings.TrimSpace(m.gatewayKeyAlias.Value())
			if err := gatewayconfig.ValidateAlias(alias); err != nil {
				m.status = err.Error()
				return m, m.gatewayKeyAlias.Focus()
			}
			store := m.gatewaySettingsStore
			value := m.gatewaySettings
			m.gatewayKeyAlias.Blur()
			m.status = "Generating API key…"
			return m, func() tea.Msg {
				updated, generated, err := store.CreateAPIKey(value, alias, time.Now())
				return gatewayAPIKeyGeneratedMsg{config: updated, key: generated.Secret, err: err}
			}
		}
		var command tea.Cmd
		m.gatewayKeyAlias, command = m.gatewayKeyAlias.Update(key)
		return m, command
	}

	keys := m.gatewaySettings.APIKeys
	switch key.String() {
	case "esc":
		m.enterGatewaySettings()
		return m, nil
	case "up":
		m.gatewayKeyRevokeArmed = false
		if m.gatewayKeyCursor > 0 {
			m.gatewayKeyCursor--
		}
		return m, nil
	case "down":
		m.gatewayKeyRevokeArmed = false
		if m.gatewayKeyCursor < len(keys)-1 {
			m.gatewayKeyCursor++
		}
		return m, nil
	case "a":
		m.gatewayKeyAdding = true
		m.gatewayKeyRevokeArmed = false
		m.gatewayKeyAlias.Reset()
		m.status = "Enter an alias for the new API key"
		return m, m.gatewayKeyAlias.Focus()
	case "r":
		if len(keys) == 0 || keys[m.gatewayKeyCursor].RevokedAt != nil {
			return m, nil
		}
		if !m.gatewayKeyRevokeArmed {
			m.gatewayKeyRevokeArmed = true
			m.status = "Press r again to revoke " + keys[m.gatewayKeyCursor].Alias
			return m, nil
		}
		store := m.gatewaySettingsStore
		value := m.gatewaySettings
		id := keys[m.gatewayKeyCursor].ID
		m.gatewayKeyRevokeArmed = false
		m.status = "Revoking API key…"
		return m, func() tea.Msg {
			updated, err := store.RevokeAPIKey(value, id, time.Now())
			return gatewayAPIKeyRevokedMsg{config: updated, err: err}
		}
	}
	m.gatewayKeyRevokeArmed = false
	return m, nil
}

func (m model) gatewayNetworkFocusCommand() tea.Cmd {
	if m.gatewayNetworkFocus == 0 {
		m.gatewayPortInput.Blur()
		return m.gatewayHostInput.Focus()
	}
	m.gatewayHostInput.Blur()
	return m.gatewayPortInput.Focus()
}

func (m *model) blurGatewayInputs() {
	m.gatewayHostInput.Blur()
	m.gatewayPortInput.Blur()
	m.gatewayKeyAlias.Blur()
}

func (m model) updateGatewayInput(message tea.Msg) (tea.Model, tea.Cmd) {
	var command tea.Cmd
	switch {
	case m.screen == screenGatewayNetwork && m.gatewayNetworkFocus == 0:
		m.gatewayHostInput, command = m.gatewayHostInput.Update(message)
	case m.screen == screenGatewayNetwork:
		m.gatewayPortInput, command = m.gatewayPortInput.Update(message)
	case m.screen == screenGatewayKeys && m.gatewayKeyAdding:
		m.gatewayKeyAlias, command = m.gatewayKeyAlias.Update(message)
	}
	return m, command
}

func (m model) viewGateway() string {
	labels := []string{"Network", "API keys", "Providers"}
	details := []string{
		net.JoinHostPort(m.gatewaySettings.Server.Host, strconv.Itoa(m.gatewaySettings.Server.Port)),
		fmt.Sprintf("%d active · %d total", m.gatewaySettings.ActiveKeyCount(), len(m.gatewaySettings.APIKeys)),
		fmt.Sprintf("%d configured", len(m.gatewayConfig.Providers)),
	}
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · Gateway settings"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render(m.gatewaySettingsStore.Path()))
	body.WriteString("\n\n")
	for index, label := range labels {
		prefix := "  "
		style := subtleStyle
		if index == m.gatewayCursor {
			prefix = "› "
			style = activeLabelStyle
		}
		body.WriteString(prefix + style.Render(label) + subtleStyle.Render(" · "+details[index]) + "\n")
	}
	if m.status != "" {
		body.WriteString("\n" + errorStyle.Render(m.status) + "\n")
	}
	body.WriteString("\n" + helpStyle.Render("↑/↓ select · enter open · esc back"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewGatewayNetwork() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · Gateway network"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render("Saved defaults are used by `q gateway`; port conflicts fall back to a random port."))
	body.WriteString("\n\n" + m.gatewayHostInput.View() + "\n\n" + m.gatewayPortInput.View() + "\n")
	if m.status != "" {
		body.WriteString("\n" + subtleStyle.Render(m.status) + "\n")
	}
	body.WriteString("\n" + helpStyle.Render("tab/↑/↓ field · enter/ctrl+s save · esc back"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}

func (m model) viewGatewayKeys() string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · Gateway API keys"))
	body.WriteString("\n")
	m.writeWorkspacePath(&body)
	body.WriteString(subtleStyle.Render("These keys authenticate standalone `q gateway`; q's supervised child uses its own temporary key."))
	body.WriteString("\n")
	if m.generatedGatewayKey != "" {
		body.WriteString(subtleStyle.Render("Copy this key now. It will not be shown again."))
		body.WriteString("\n\n" + activeLabelStyle.Render(m.generatedGatewayKey) + "\n\n")
		body.WriteString(helpStyle.Render("enter/esc dismiss"))
		return frameStyle.Width(max(36, m.width-4)).Render(body.String())
	}
	if m.gatewayKeyAdding {
		body.WriteString(subtleStyle.Render("Choose a unique alias."))
		body.WriteString("\n\n" + m.gatewayKeyAlias.View() + "\n")
		if m.status != "" {
			body.WriteString("\n" + errorStyle.Render(m.status) + "\n")
		}
		body.WriteString("\n" + helpStyle.Render("enter generate · esc cancel"))
		return frameStyle.Width(max(36, m.width-4)).Render(body.String())
	}
	if len(m.gatewaySettings.APIKeys) == 0 {
		body.WriteString("\n" + emptyStyle.Render("No API keys configured") + "\n")
	} else {
		body.WriteString("\n")
		for index, key := range m.gatewaySettings.APIKeys {
			prefix := "  "
			style := subtleStyle
			if index == m.gatewayKeyCursor {
				prefix = "› "
				style = activeLabelStyle
			}
			state := "active"
			if key.RevokedAt != nil {
				state = "revoked"
			}
			shortID := key.ID
			if len(shortID) > 8 {
				shortID = shortID[:8]
			}
			body.WriteString(prefix + style.Render(key.Alias) + subtleStyle.Render(" · "+shortID+" · "+state) + "\n")
		}
	}
	if m.status != "" {
		body.WriteString("\n" + subtleStyle.Render(m.status) + "\n")
	}
	body.WriteString("\n" + helpStyle.Render("↑/↓ select · a generate · r revoke · esc back"))
	return frameStyle.Width(max(36, m.width-4)).Render(body.String())
}
