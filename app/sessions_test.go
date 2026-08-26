package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

func TestSessionPickerRendersTitlesTimesAndEmptyFallback(t *testing.T) {
	root := t.TempDir()
	older := savePickerSession(t, root, "older", "Review the parser", time.Date(2026, 8, 26, 12, 30, 0, 0, time.Local))
	newer := savePickerSession(t, root, "newer", "", time.Date(2026, 8, 27, 20, 34, 0, 0, time.Local))
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.screen = screenSessions
	m.workspaceStore = &workspace.Store{Root: root}
	m.sessions = []workspace.SessionEntry{newer, older}
	m.resize(90, 24)

	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"q · Sessions", "(untitled)", "Review the parser", "2026-08-27 20:34", "newer", "older"} {
		if !strings.Contains(view, want) {
			t.Fatalf("session picker missing %q:\n%s", want, view)
		}
	}
}

func TestSessionPickerSelectionRestoresSessionAndCancelsOldLearning(t *testing.T) {
	root := t.TempDir()
	current, currentLock, err := workspace.CreateSession(root, "test current")
	if err != nil {
		t.Fatal(err)
	}
	target := savePickerSession(t, root, "target-session", "Target conversation", time.Now())
	targetSession, err := target.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	targetSession.Transcript = []client.Message{{Role: client.RoleUser, Content: "restored question"}}
	targetSession.Context = append([]client.Message(nil), targetSession.Transcript...)
	if err := target.Store.Save(targetSession); err != nil {
		t.Fatal(err)
	}

	value := config.Default()
	value.Provider.Model = "test-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &current
	m.workspaceLock = currentLock
	m.config = value
	m.client = &fakeClient{}
	m.screen = screenSessions
	m.sessions = []workspace.SessionEntry{target}
	oldLearningContext := m.learningCtx

	updated, _ := m.updateSessions(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	t.Cleanup(func() {
		if m.workspaceLock != nil {
			_ = m.workspaceLock.Close()
		}
	})

	if m.screen != screenChat || m.workspaceStore == nil || m.workspaceStore.SessionID != target.Store.SessionID {
		t.Fatalf("selected session state = screen %v store %#v", m.screen, m.workspaceStore)
	}
	if m.sessionTitle != "Target conversation" || !containsMessageText(m.messages, "restored question") {
		t.Fatalf("restored title/messages = %q, %#v", m.sessionTitle, m.messages)
	}
	select {
	case <-oldLearningContext.Done():
	default:
		t.Fatal("switching sessions did not cancel the previous learning context")
	}
	if contender, err := workspace.AcquireSessionLock(root, current.SessionID, "verify released"); err != nil {
		t.Fatalf("previous session lock was not released: %v", err)
	} else {
		_ = contender.Close()
	}
}

func TestSessionPickerKeepsCurrentSessionWhenTargetIsLocked(t *testing.T) {
	root := t.TempDir()
	current, currentLock, err := workspace.CreateSession(root, "test current")
	if err != nil {
		t.Fatal(err)
	}
	targetStore, targetLock, err := workspace.CreateSession(root, "test target")
	if err != nil {
		t.Fatal(err)
	}
	defer targetLock.Close()
	targetSession, err := targetStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	target := workspace.SessionEntry{Store: targetStore, RunID: targetSession.RunID, UpdatedAt: time.Now()}

	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &current
	m.workspaceLock = currentLock
	m.client = &fakeClient{}
	m.screen = screenSessions
	m.sessions = []workspace.SessionEntry{target}
	updated, _ := m.updateSessions(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	defer m.workspaceLock.Close()

	if m.screen != screenSessions || m.workspaceStore.SessionID != current.SessionID || m.workspaceLock != currentLock {
		t.Fatalf("locked selection replaced current state: screen %v store %#v", m.screen, m.workspaceStore)
	}
	if !strings.Contains(m.status, "another q process") {
		t.Fatalf("locked selection status = %q", m.status)
	}
	if _, err := workspace.AcquireSessionLock(root, current.SessionID, "verify retained"); !errors.Is(err, workspace.ErrLocked) {
		t.Fatalf("current session lock was not retained: %v", err)
	}
}

func TestSessionsCommandOpensPickerOnCurrentSession(t *testing.T) {
	root := t.TempDir()
	current, currentLock, err := workspace.CreateSession(root, "test current")
	if err != nil {
		t.Fatal(err)
	}
	value := config.Default()
	value.Provider.Model = "test-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &current
	m.workspaceLock = currentLock
	m.enterChat(value, &fakeClient{})
	m.input.SetValue("/sessions")

	updated, _ := m.submitChat()
	m = updated.(model)
	defer m.workspaceLock.Close()
	if m.screen != screenSessions || len(m.sessions) != 1 ||
		m.sessions[m.sessionCursor].Store.SessionID != current.SessionID {
		t.Fatalf("/sessions state = screen %v cursor %d sessions %#v", m.screen, m.sessionCursor, m.sessions)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "current") {
		t.Fatalf("picker did not mark current session:\n%s", view)
	}
}

func TestSessionPickerSelectionDuringStartupRestoresAfterRuntimeReady(t *testing.T) {
	root := t.TempDir()
	entry := savePickerSession(t, root, "startup", "Resume after startup", time.Now())
	session, err := entry.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	session.Transcript = []client.Message{{Role: client.RoleUser, Content: "persisted startup message"}}
	if err := entry.Store.Save(session); err != nil {
		t.Fatal(err)
	}

	value := config.Default()
	value.Provider.Model = "startup-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspace.Store{Root: root}
	m.sessions = []workspace.SessionEntry{entry}
	m.sessionPickerRequired = true
	m.screen = screenSessions
	m.initializing = true
	updated, _ := m.updateSessions(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.screen != screenChat || m.workspaceStore.SessionID != entry.Store.SessionID || !m.initializing {
		t.Fatalf("startup selection = screen %v store %#v initializing %v", m.screen, m.workspaceStore, m.initializing)
	}

	configuredClient := &fakeClient{}
	updated, _ = m.Update(runtimeInitializedMsg{config: value, client: configuredClient})
	m = updated.(model)
	defer m.workspaceLock.Close()
	if m.initializing || m.screen != screenChat || m.client != configuredClient ||
		m.sessionTitle != "Resume after startup" || !containsMessageText(m.messages, "persisted startup message") {
		t.Fatalf("runtime restore = initializing %v screen %v client %#v title %q messages %#v", m.initializing, m.screen, m.client, m.sessionTitle, m.messages)
	}
}

func TestRuntimeReadyWaitsForStartupSessionSelection(t *testing.T) {
	root := t.TempDir()
	entry := savePickerSession(t, root, "waiting", "Choose me", time.Now())
	value := config.Default()
	value.Provider.Model = "ready-model"
	configuredClient := &fakeClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspace.Store{Root: root}
	m.sessions = []workspace.SessionEntry{entry}
	m.sessionPickerRequired = true
	m.screen = screenSessions
	m.initializing = true

	updated, _ := m.Update(runtimeInitializedMsg{config: value, client: configuredClient})
	m = updated.(model)
	if m.initializing || m.screen != screenSessions || m.client != configuredClient || m.workspaceStore.SessionID != "" {
		t.Fatalf("runtime skipped picker: initializing %v screen %v client %#v store %#v", m.initializing, m.screen, m.client, m.workspaceStore)
	}
	updated, _ = m.updateSessions(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	defer m.workspaceLock.Close()
	if m.screen != screenChat || m.workspaceStore.SessionID != entry.Store.SessionID || m.sessionTitle != "Choose me" {
		t.Fatalf("post-runtime selection = screen %v store %#v title %q", m.screen, m.workspaceStore, m.sessionTitle)
	}
}

func TestStartupHelpDoesNotBypassRequiredSessionSelection(t *testing.T) {
	root := t.TempDir()
	value := config.Default()
	value.Provider.Model = "ready-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspace.Store{Root: root}
	m.sessionPickerRequired = true
	m.screen = screenSessions
	m.initializing = true
	updated, _ := m.enterHelp()
	m = updated.(model)

	updated, _ = m.Update(runtimeInitializedMsg{config: value, client: &fakeClient{}})
	m = updated.(model)
	if m.screen != screenHelp || !m.sessionPickerRequired || m.workspaceStore.SessionID != "" {
		t.Fatalf("runtime bypassed picker behind help: screen %v required %v store %#v", m.screen, m.sessionPickerRequired, m.workspaceStore)
	}
	updated, _ = m.leaveHelp()
	m = updated.(model)
	if m.screen != screenSessions {
		t.Fatalf("help returned to screen %v, want sessions", m.screen)
	}
}

func TestNewSessionFailureLeavesCurrentStateAndLock(t *testing.T) {
	root := t.TempDir()
	current, currentLock, err := workspace.CreateSession(root, "test current")
	if err != nil {
		t.Fatal(err)
	}
	sessionsDir := filepath.Join(root, workspace.DirectoryName, workspace.SessionsName)
	movedSessionsDir := sessionsDir + "-held"
	if err := os.Rename(sessionsDir, movedSessionsDir); err != nil {
		currentLock.Close()
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionsDir, []byte("block directory creation"), 0o600); err != nil {
		currentLock.Close()
		t.Fatal(err)
	}

	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &current
	m.workspaceLock = currentLock
	m.screen = screenSessions
	updated, _ := m.updateSessions(tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = updated.(model)
	defer m.workspaceLock.Close()
	if m.workspaceStore.SessionID != current.SessionID || m.workspaceLock != currentLock || m.status == "" {
		t.Fatalf("failed creation replaced current state: store %#v lock %p status %q", m.workspaceStore, m.workspaceLock, m.status)
	}
}

func TestSessionWindowKeepsCursorVisible(t *testing.T) {
	for _, cursor := range []int{0, 5, 19} {
		start, end := sessionWindow(cursor, 20, sessionVisibleRows(18, true))
		if start < 0 || end > 20 || start > cursor || cursor >= end {
			t.Fatalf("window for cursor %d = [%d,%d)", cursor, start, end)
		}
	}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspace.Store{Root: t.TempDir()}
	m.screen = screenSessions
	m.status = "Starting workspace services…"
	for index := range 20 {
		m.sessions = append(m.sessions, workspace.SessionEntry{
			Store: workspace.Store{Root: m.workspaceStore.Root, SessionID: fmt.Sprintf("session-%02d", index)},
			Title: fmt.Sprintf("Session %02d", index), UpdatedAt: time.Now(),
		})
	}
	m.sessionCursor = 19
	m.resize(40, 12)
	if height := lipgloss.Height(ansi.Strip(m.View().Content)); height > m.height {
		t.Fatalf("minimum-size picker height = %d, terminal height = %d\n%s", height, m.height, ansi.Strip(m.View().Content))
	}
}

func savePickerSession(t *testing.T, root, id, title string, updatedAt time.Time) workspace.SessionEntry {
	t.Helper()
	store, err := (workspace.Store{Root: root}).ForSession(id)
	if err != nil {
		t.Fatal(err)
	}
	updatedAt = updatedAt.UTC()
	if err := store.Save(workspace.Session{
		ID: id, RunID: "run-" + id, Title: title, UpdatedAt: &updatedAt,
	}); err != nil {
		t.Fatal(err)
	}
	return workspace.SessionEntry{Store: store, RunID: "run-" + id, Title: title, UpdatedAt: updatedAt}
}

func containsMessageText(messages []client.Message, content string) bool {
	for _, message := range messages {
		if message.TextContent() == content {
			return true
		}
	}
	return false
}
