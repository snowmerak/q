package app

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/workspace"
)

// enterSessions opens the workspace session picker without releasing the
// current session. The old lock remains held until another session has been
// locked and revalidated, so a failed selection returns to an intact session.
func (m model) enterSessions() (tea.Model, tea.Cmd) {
	if m.clientIsACPRemote() {
		m.status = "Session selection is controlled by the connected ACP client"
		return m, m.input.Focus()
	}
	if m.workspaceStore == nil {
		m.status = "Workspace session storage is unavailable"
		return m, m.input.Focus()
	}
	if m.waiting || m.planResumePending {
		m.status = "Finish or interrupt the active turn before switching sessions"
		return m, nil
	}
	if m.workspaceStore.SessionID != "" {
		if err := m.saveWorkspaceSession(); err != nil {
			m.status = err.Error()
			return m, m.input.Focus()
		}
	}
	if err := m.refreshSessions(); err != nil {
		m.status = err.Error()
		return m, m.input.Focus()
	}
	m.screen = screenSessions
	m.status = ""
	m.input.Blur()
	return m, nil
}

func (m *model) refreshSessions() error {
	if m.workspaceStore == nil {
		return errors.New("workspace session storage is unavailable")
	}
	entries, err := (workspace.Store{Root: m.workspaceStore.Root}).ListSessions()
	if err != nil {
		return err
	}
	m.sessions = entries
	m.sessionCursor = min(m.sessionCursor, max(0, len(entries)-1))
	currentID := m.workspaceStore.SessionID
	if currentID != "" {
		for index, entry := range entries {
			if entry.Store.SessionID == currentID {
				m.sessionCursor = index
				break
			}
		}
	}
	return nil
}

func (m model) updateSessions(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "up", "k", "shift+tab":
		if m.sessionCursor > 0 {
			m.sessionCursor--
		}
		return m, nil
	case "down", "j", "tab":
		if m.sessionCursor < len(m.sessions)-1 {
			m.sessionCursor++
		}
		return m, nil
	case "r":
		if err := m.refreshSessions(); err != nil {
			m.status = err.Error()
		} else {
			m.status = "Session list refreshed"
		}
		return m, nil
	case "n":
		return m.createSessionFromPicker()
	case "enter", " ":
		if len(m.sessions) == 0 {
			return m.createSessionFromPicker()
		}
		return m.selectSessionFromPicker(m.sessions[m.sessionCursor])
	case "esc":
		if m.workspaceStore != nil && m.workspaceStore.SessionID != "" && m.client != nil {
			m.screen = screenChat
			m.status = ""
			m.resize(m.width, m.height)
			m.refreshTranscript()
			return m, m.input.Focus()
		}
		return m, tea.Quit
	}
	return m, nil
}

func (m model) createSessionFromPicker() (tea.Model, tea.Cmd) {
	if m.workspaceStore == nil {
		m.status = "Workspace session storage is unavailable"
		return m, nil
	}
	store, lock, err := workspace.CreateSession(m.workspaceStore.Root, "q")
	if err != nil {
		m.status = err.Error()
		return m, nil
	}
	return m.activatePickedSession(store, lock, "New workspace session")
}

func (m model) selectSessionFromPicker(entry workspace.SessionEntry) (tea.Model, tea.Cmd) {
	if m.workspaceStore == nil {
		m.status = "Workspace session storage is unavailable"
		return m, nil
	}
	if entry.Store.SessionID == m.workspaceStore.SessionID && m.workspaceLock != nil {
		m.screen = screenChat
		m.status = "Current workspace session"
		m.resize(m.width, m.height)
		m.refreshTranscript()
		return m, m.input.Focus()
	}
	lock, err := workspace.AcquireSessionLock(m.workspaceStore.Root, entry.Store.SessionID, "q")
	if err != nil {
		if errors.Is(err, workspace.ErrLocked) {
			m.status = "Session is open in another q process"
		} else {
			m.status = err.Error()
		}
		return m, nil
	}
	// Revalidate after taking the session lock; list data may have changed while
	// the picker was visible.
	if _, err := entry.Store.Load(); err != nil {
		_ = lock.Close()
		m.status = err.Error()
		return m, nil
	}
	return m.activatePickedSession(entry.Store, lock, "Workspace session resumed")
}

func (m model) activatePickedSession(store workspace.Store, lock *workspace.Lock, status string) (tea.Model, tea.Cmd) {
	previousLock := m.workspaceLock
	m.workspaceStore = &store
	m.workspaceLock = lock
	m.sessionPickerRequired = false
	m.workspaceRestored = false

	closePrevious := func() {
		if previousLock == nil || previousLock == lock {
			return
		}
		if err := previousLock.Close(); err != nil {
			m.status += " · release previous session: " + err.Error()
		}
	}

	if m.initializing {
		m.screen = screenChat
		m.status = status + " · starting workspace services…"
		m.resize(m.width, m.height)
		closePrevious()
		return m, m.spinner.Tick
	}
	if m.client != nil {
		m.enterChat(m.config, m.client)
		m.status = status + " · " + sessionDisplayTitle(m.sessionTitle)
		m.resize(m.width, m.height)
		closePrevious()
		return m, tea.Batch(m.input.Focus(), m.startNextLearningSegment())
	}
	closePrevious()
	if len(m.gatewayConfig.Providers) > 0 {
		m.enterProviderList()
		return m, nil
	}
	m.enterSetup(m.config)
	return m, m.setup[m.setupFocus].Focus()
}

func (m model) viewSessions() string {
	var body strings.Builder
	viewWidth := max(36, m.width-4)
	contentWidth := max(24, viewWidth-frameStyle.GetHorizontalFrameSize())
	body.WriteString(titleStyle.Render("q · Sessions"))
	body.WriteString("\n")
	if m.workspaceStore != nil {
		path := "workspace · " + filepath.Clean(m.workspaceStore.Root)
		body.WriteString(subtleStyle.Render(ansi.Truncate(path, contentWidth, "…")))
		body.WriteString("\n")
	}
	description := "Choose a previous conversation or start a new one."
	if len(m.sessions) > 0 {
		description += fmt.Sprintf(" · %d/%d", m.sessionCursor+1, len(m.sessions))
	}
	body.WriteString(subtleStyle.Render(ansi.Truncate(description, contentWidth, "…")))
	body.WriteString("\n\n")

	if len(m.sessions) == 0 {
		body.WriteString(emptyStyle.Render("No saved sessions yet. Press enter or n to create one."))
	} else {
		visible := sessionVisibleRows(m.height, m.status != "")
		start, end := sessionWindow(m.sessionCursor, len(m.sessions), visible)
		for index := start; index < end; index++ {
			entry := m.sessions[index]
			selected := index == m.sessionCursor
			prefix := "  "
			style := subtleStyle
			if selected {
				prefix = "› "
				style = activeLabelStyle
			}
			title := ansi.Truncate(sessionDisplayTitle(entry.Title), contentWidth-2, "…")
			body.WriteString(style.Render(prefix + title))
			body.WriteString("\n")
			metadata := "    updated " + formatSessionTime(entry.UpdatedAt) + " · " + shortSessionID(entry.Store.SessionID)
			if m.workspaceStore != nil && entry.Store.SessionID == m.workspaceStore.SessionID && m.workspaceLock != nil {
				metadata = "    current · updated " + formatSessionTime(entry.UpdatedAt) + " · " + shortSessionID(entry.Store.SessionID)
			}
			body.WriteString(subtleStyle.Render(ansi.Truncate(metadata, contentWidth, "…")))
			if index < end-1 {
				body.WriteString("\n")
			}
		}
	}

	if m.status != "" {
		body.WriteString("\n\n")
		status := m.status
		if m.initializing {
			status = m.spinner.View() + " " + status
		}
		body.WriteString(subtleStyle.Render(ansi.Truncate(status, max(20, m.width-8), "…")))
	}
	body.WriteString("\n\n")
	footer := "↑/↓ select · enter resume · n new · r refresh · esc quit · ctrl+h help"
	if m.workspaceStore != nil && m.workspaceStore.SessionID != "" && m.client != nil {
		footer = "↑/↓ select · enter resume · n new · r refresh · esc back · ctrl+h help"
	}
	body.WriteString(helpStyle.Render(ansi.Truncate(footer, contentWidth, "…")))
	return frameStyle.Width(viewWidth).Render(body.String())
}

func sessionVisibleRows(height int, hasStatus bool) int {
	fixedRows := 8 // three header rows, gaps/footer, and the frame's vertical padding
	if hasStatus {
		fixedRows += 2
	}
	return max(1, (max(height, 12)-fixedRows)/2)
}

func sessionWindow(cursor, count, visible int) (int, int) {
	visible = max(1, visible)
	visible = min(visible, count)
	start := max(0, cursor-visible/2)
	start = min(start, max(0, count-visible))
	return start, start + visible
}

func sessionDisplayTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "(untitled)"
	}
	return title
}

func shortSessionID(sessionID string) string {
	if len(sessionID) <= 8 {
		return sessionID
	}
	return sessionID[:8]
}

func formatSessionTime(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.Local().Format("2006-01-02 15:04")
}
