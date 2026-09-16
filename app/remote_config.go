package app

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/remoteconfig"
)

type remoteConfigScreen uint8

const (
	remoteConfigHome remoteConfigScreen = iota
	remoteConfigNetwork
	remoteConfigAlias
	remoteConfigSecret
)

type remoteConfigModel struct {
	store        remoteconfig.Store
	value        remoteconfig.Config
	screen       remoteConfigScreen
	host         textinput.Model
	port         textinput.Model
	alias        textinput.Model
	networkFocus int
	keyCursor    int
	status       string
	secret       string
	width        int
}

func newRemoteConfigModel(store remoteconfig.Store) remoteConfigModel {
	value, err := store.LoadOrDefault()
	host := textinput.New()
	host.Prompt = "host · "
	host.SetWidth(40)
	port := textinput.New()
	port.Prompt = "port · "
	port.CharLimit = 5
	port.SetWidth(16)
	alias := textinput.New()
	alias.Prompt = "alias · "
	alias.CharLimit = 64
	alias.SetWidth(48)
	model := remoteConfigModel{store: store, value: value, host: host, port: port, alias: alias, width: 80}
	if err != nil {
		model.status = err.Error()
	}
	return model
}

func (m remoteConfigModel) Init() tea.Cmd { return tea.RequestBackgroundColor }

func (m remoteConfigModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		return m, nil
	case tea.KeyPressMsg:
		switch m.screen {
		case remoteConfigNetwork:
			return m.updateRemoteNetwork(message)
		case remoteConfigAlias:
			return m.updateRemoteAlias(message)
		case remoteConfigSecret:
			if message.String() == "enter" || message.String() == "esc" {
				m.screen = remoteConfigHome
				m.secret = ""
				m.status = "API key generated"
			}
			return m, nil
		default:
			return m.updateRemoteHome(message)
		}
	}
	return m, nil
}

func (m remoteConfigModel) updateRemoteHome(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "n":
		m.screen = remoteConfigNetwork
		m.networkFocus = 0
		m.host.SetValue(m.value.Server.Host)
		m.port.SetValue(strconv.Itoa(m.value.Server.Port))
		m.port.Blur()
		m.status = ""
		return m, m.host.Focus()
	case " ":
		candidate := m.value
		candidate.Authentication.Enabled = !candidate.Authentication.Enabled
		if err := m.store.Save(candidate); err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.value = candidate
		m.status = "Authentication setting saved; running servers reload it automatically"
		return m, nil
	case "a":
		m.screen = remoteConfigAlias
		m.alias.Reset()
		m.status = "Enter a unique alias"
		return m, m.alias.Focus()
	case "up", "k":
		if m.keyCursor > 0 {
			m.keyCursor--
		}
		return m, nil
	case "down", "j":
		if m.keyCursor < len(m.value.APIKeys)-1 {
			m.keyCursor++
		}
		return m, nil
	case "r":
		if len(m.value.APIKeys) == 0 || m.value.APIKeys[m.keyCursor].RevokedAt != nil {
			return m, nil
		}
		updated, err := m.store.RevokeAPIKey(m.value, m.value.APIKeys[m.keyCursor].ID, time.Now())
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.value = updated
		m.status = "API key revoked"
		return m, nil
	}
	return m, nil
}

func (m remoteConfigModel) updateRemoteNetwork(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.host.Blur()
		m.port.Blur()
		m.screen = remoteConfigHome
		m.status = ""
		return m, nil
	case "tab", "shift+tab", "up", "down":
		m.networkFocus = 1 - m.networkFocus
		if m.networkFocus == 0 {
			m.port.Blur()
			return m, m.host.Focus()
		}
		m.host.Blur()
		return m, m.port.Focus()
	case "enter":
		host := strings.TrimSpace(m.host.Value())
		port, err := strconv.Atoi(strings.TrimSpace(m.port.Value()))
		if net.ParseIP(host) == nil {
			m.status = "Host must be an IP address"
			return m, nil
		}
		if err != nil || port < 0 || port > 65535 {
			m.status = "Port must be between 0 and 65535"
			return m, nil
		}
		candidate := m.value
		candidate.Server = remoteconfig.ServerConfig{Host: host, Port: port}
		if err := m.store.Save(candidate); err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.value = candidate
		m.screen = remoteConfigHome
		m.host.Blur()
		m.port.Blur()
		m.status = "Network settings saved; restart q remote to apply them"
		return m, nil
	}
	var command tea.Cmd
	if m.networkFocus == 0 {
		m.host, command = m.host.Update(key)
	} else {
		m.port, command = m.port.Update(key)
	}
	return m, command
}

func (m remoteConfigModel) updateRemoteAlias(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.alias.Blur()
		m.screen = remoteConfigHome
		m.status = ""
		return m, nil
	case "enter":
		alias := strings.TrimSpace(m.alias.Value())
		if err := remoteconfig.ValidateAlias(alias); err != nil {
			m.status = err.Error()
			return m, nil
		}
		updated, generated, err := m.store.CreateAPIKey(m.value, alias, time.Now())
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.value = updated
		m.secret = generated.Secret
		m.alias.Blur()
		m.screen = remoteConfigSecret
		m.status = ""
		return m, nil
	}
	var command tea.Cmd
	m.alias, command = m.alias.Update(key)
	return m, command
}

func (m remoteConfigModel) View() tea.View {
	var body strings.Builder
	body.WriteString(titleStyle.Render("q · Remote settings"))
	body.WriteString("\n")
	body.WriteString(subtleStyle.Render(m.store.Path()))
	body.WriteString("\n\n")
	switch m.screen {
	case remoteConfigNetwork:
		body.WriteString("Network\n\n")
		body.WriteString(m.host.View())
		body.WriteString("\n\n")
		body.WriteString(m.port.View())
		body.WriteString("\n\n")
		body.WriteString(helpStyle.Render("tab/↑/↓ field · enter save · esc back"))
	case remoteConfigAlias:
		body.WriteString("Generate Remote API key\n\n")
		body.WriteString(m.alias.View())
		body.WriteString("\n\n")
		body.WriteString(helpStyle.Render("enter generate · esc cancel"))
	case remoteConfigSecret:
		body.WriteString("Copy this key now. It will not be shown again.\n\n")
		body.WriteString(activeLabelStyle.Render(m.secret))
		body.WriteString("\n\n")
		body.WriteString(helpStyle.Render("enter/esc dismiss"))
	default:
		authentication := "disabled"
		if m.value.Authentication.Enabled {
			authentication = "enabled"
		}
		fmt.Fprintf(&body, "Network · %s\n", net.JoinHostPort(m.value.Server.Host, strconv.Itoa(m.value.Server.Port)))
		fmt.Fprintf(&body, "Authentication · %s · press space to toggle\n", authentication)
		if !m.value.ServerIsLoopback() {
			warning := "Warning: non-loopback Remote exposes agent execution; use API keys and confidential transport."
			if !m.value.Authentication.Enabled {
				warning = "Warning: non-loopback Remote is unauthenticated."
			}
			body.WriteString(errorStyle.Render(warning))
			body.WriteString("\n")
		}
		body.WriteString("\nAPI keys\n")
		if len(m.value.APIKeys) == 0 {
			body.WriteString(emptyStyle.Render("  No API keys configured"))
			body.WriteString("\n")
		}
		for index, key := range m.value.APIKeys {
			prefix := "  "
			style := subtleStyle
			if index == m.keyCursor {
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
		body.WriteString("\n")
		body.WriteString(helpStyle.Render("n network · space authentication · a generate · ↑/↓ select · r revoke · q quit"))
	}
	if m.status != "" {
		body.WriteString("\n\n")
		body.WriteString(subtleStyle.Render(m.status))
	}
	view := tea.NewView(frameStyle.Width(max(36, m.width-4)).Render(body.String()))
	return view
}

func RunRemoteConfig(ctx context.Context, store config.Store) error {
	_, err := tea.NewProgram(newRemoteConfigModel(remoteconfig.Store{Dir: store.Dir}), tea.WithContext(ctx)).Run()
	return err
}

func RunRemoteConfigDefault(ctx context.Context) error {
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	if err := RunRemoteConfig(ctx, store); err != nil {
		return fmt.Errorf("q remote config: %w", err)
	}
	return nil
}
