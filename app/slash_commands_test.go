package app

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/config"
)

func newSlashCompletionModel(t *testing.T) model {
	t.Helper()
	value := config.Default()
	value.Provider.Model = "test-model"
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, &fakeClient{})
	m.resize(100, 30)
	return m
}

func completionNames(commands []slashCommand) []string {
	var names []string
	for _, command := range commands {
		names = append(names, command.name)
	}
	return names
}

func completionKey(m model, key tea.KeyPressMsg) (model, tea.Cmd) {
	updated, command := m.Update(key)
	return updated.(model), command
}

func TestSlashCompletionOpensAndFiltersWhileTyping(t *testing.T) {
	m := newSlashCompletionModel(t)
	m, _ = completionKey(m, tea.KeyPressMsg{Code: '/', Text: "/"})
	if matches := m.slashCompletionMatches(); len(matches) != len(localSlashCommands) {
		t.Fatalf("slash matches = %#v", matches)
	}
	view := ansi.Strip(m.View().Content)
	for _, expected := range []string{"Commands", "/plan [request]", "Grill, research", "tab/enter complete", "esc close"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("popup missing %q:\n%s", expected, view)
		}
	}
	m, _ = completionKey(m, tea.KeyPressMsg{Code: 'm', Text: "m"})
	if names := completionNames(m.slashCompletionMatches()); !slices.Equal(names, []string{"/model", "/mcp"}) {
		t.Fatalf("/m matches = %v", names)
	}
	m, _ = completionKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = completionKey(m, tea.KeyPressMsg{Code: 'o', Text: "o"})
	if names := completionNames(m.slashCompletionMatches()); !slices.Equal(names, []string{"/model"}) || m.slashCompletion.selected != 0 {
		t.Fatalf("/mo matches = %v, selection = %d", names, m.slashCompletion.selected)
	}
}

func TestSlashCompletionNavigationDoesNotScrollTranscript(t *testing.T) {
	m := newSlashCompletionModel(t)
	m.viewport.SetContent(strings.Repeat("transcript line\n", 100))
	m.viewport.SetYOffset(7)
	m.input.SetValue("/")
	offset, column := m.viewport.YOffset(), m.input.Column()
	m, _ = completionKey(m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.slashCompletion.selected != len(localSlashCommands)-1 {
		t.Fatalf("up did not wrap: %d", m.slashCompletion.selected)
	}
	if !strings.Contains(ansi.Strip(m.renderSlashCompletion(70, 8)), "› /help") {
		t.Fatal("popup did not scroll to the selected command")
	}
	m, _ = completionKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.slashCompletion.selected != 0 {
		t.Fatal("down did not wrap to the first command")
	}
	m, _ = completionKey(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.slashCompletion.selected != len(localSlashCommands)-1 || m.viewport.YOffset() != offset || m.input.Column() != column {
		t.Fatalf("selection navigation moved the transcript or input: selected=%d offset=%d column=%d", m.slashCompletion.selected, m.viewport.YOffset(), m.input.Column())
	}
	m, _ = completionKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.input.Value() != "/help" || len(m.slashCompletionMatches()) != 0 || m.submitPending || m.screen != screenChat {
		t.Fatalf("Tab did not only complete the input: %q", m.input.Value())
	}
}

func TestSlashCompletionEnterCompletesThenRuns(t *testing.T) {
	m := newSlashCompletionModel(t)
	m.input.SetValue("/he")
	m, command := completionKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.input.Value() != "/help" || m.screen != screenChat || m.submitPending || command != nil {
		t.Fatal("Enter ran an incomplete command instead of completing it")
	}
	m, command = completionKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.submitPending || command == nil {
		t.Fatal("second Enter did not schedule the completed command")
	}
	updated, _ := m.Update(deferredSubmitMsg{})
	m = updated.(model)
	if m.screen != screenHelp || m.input.Value() != "" {
		t.Fatal("completed command was not dispatched through the existing handler")
	}

	m = newSlashCompletionModel(t)
	m.input.SetValue("/help")
	m, command = completionKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.submitPending || command == nil || len(m.slashCompletionMatches()) != 0 {
		t.Fatal("a fully typed command no longer runs on the first Enter")
	}
}

func TestSlashCompletionKeepsArgumentsEditable(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyTab}, {Code: tea.KeyEnter}} {
		t.Run(key.String(), func(t *testing.T) {
			m := newSlashCompletionModel(t)
			m.input.SetValue("/agent:se")
			m, _ = completionKey(m, key)
			if m.input.Value() != "/agent:search " || m.input.Column() != len("/agent:search ") || m.submitPending {
				t.Fatalf("completion did not leave room for arguments: %q", m.input.Value())
			}
			updated, _ := m.Update(tea.PasteMsg{Content: "한글 query"})
			m = updated.(model)
			if m.input.Value() != "/agent:search 한글 query" || len(m.slashCompletionMatches()) != 0 {
				t.Fatalf("arguments were changed or reopened completion: %q", m.input.Value())
			}
		})
	}
}

func TestSlashCompletionEscapeDismissesUntilInputChanges(t *testing.T) {
	m := newSlashCompletionModel(t)
	m.input.SetValue("/")
	m, command := completionKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if command != nil || m.input.Value() != "/" || len(m.slashCompletionMatches()) != 0 {
		t.Fatal("Escape quit or changed the input instead of dismissing completion")
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 25})
	m = updated.(model)
	if len(m.slashCompletionMatches()) != 0 {
		t.Fatal("resize reopened a dismissed popup")
	}
	m, _ = completionKey(m, tea.KeyPressMsg{Code: 'm', Text: "m"})
	if len(m.slashCompletionMatches()) != 2 {
		t.Fatal("editing did not reopen the filtered popup")
	}
	m, _ = completionKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	_, command = completionKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if command == nil {
		t.Fatal("Escape outside the popup no longer quits")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatal("Escape outside the popup returned a non-quit command")
	}
}

func TestSlashCompletionOnlyHandlesCommandInput(t *testing.T) {
	for _, value := range []string{"", "normal message", "use /model", " /model", "/unknown", "/tmp/file", "/plan request", "/learn off", "/\n", "/model\t"} {
		t.Run(fmt.Sprintf("input=%q", value), func(t *testing.T) {
			m := newSlashCompletionModel(t)
			m.input.SetValue(value)
			if len(m.slashCompletionMatches()) != 0 {
				t.Fatalf("popup opened for non-command input %q", value)
			}
		})
	}
	for _, state := range []string{"waiting", "asking", "initializing", "submitting", "other screen", "blurred"} {
		t.Run(state, func(t *testing.T) {
			m := newSlashCompletionModel(t)
			m.input.SetValue("/")
			switch state {
			case "waiting":
				m.waiting = true
			case "asking":
				m.asking = true
			case "initializing":
				m.initializing = true
			case "submitting":
				m.submitPending = true
			case "other screen":
				m.screen = screenHelp
			case "blurred":
				m.input.Blur()
			}
			if m.handleSlashCompletionKey(tea.KeyPressMsg{Code: tea.KeyTab}) || len(m.slashCompletionMatches()) != 0 || m.input.Value() != "/" {
				t.Fatal("completion intercepted input outside idle chat")
			}
		})
	}
}

func TestSlashCompletionPasteCursorAndNewline(t *testing.T) {
	m := newSlashCompletionModel(t)
	updated, _ := m.Update(tea.PasteMsg{Content: "/mc"})
	m = updated.(model)
	if names := completionNames(m.slashCompletionMatches()); !slices.Equal(names, []string{"/mcp"}) {
		t.Fatalf("paste did not filter completion: %v", names)
	}
	m, _ = completionKey(m, tea.KeyPressMsg{Code: tea.KeyLeft})
	if len(m.slashCompletionMatches()) != 0 {
		t.Fatal("completion intercepted editing in the middle of the input")
	}
	m, _ = completionKey(m, tea.KeyPressMsg{Code: tea.KeyRight})
	if len(m.slashCompletionMatches()) != 1 {
		t.Fatal("moving back to the command end did not reopen completion")
	}
	m, _ = completionKey(m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	if m.input.Value() != "/mc\n" || len(m.slashCompletionMatches()) != 0 || m.submitPending {
		t.Fatalf("Shift+Enter was consumed by completion: %q", m.input.Value())
	}
}

func TestSlashCompletionOverlayKeepsLayoutAndCursor(t *testing.T) {
	for _, size := range [][2]int{{40, 12}, {60, 18}, {100, 30}, {140, 40}} {
		for _, dark := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dx%d/dark=%t", size[0], size[1], dark), func(t *testing.T) {
				m := newSlashCompletionModel(t)
				m.applyColorScheme(dark)
				m.resize(size[0], size[1])
				m.input.SetValue("/m")
				m.syncSlashCompletion()
				m.slashCompletion.dismissed = true
				base := m.View()
				m.slashCompletion.dismissed = false
				popup := m.View()
				if base.Cursor == nil || popup.Cursor == nil || base.Cursor.Position != popup.Cursor.Position {
					t.Fatalf("popup moved the IME cursor: base=%+v popup=%+v", base.Cursor, popup.Cursor)
				}
				if lipgloss.Width(base.Content) != lipgloss.Width(popup.Content) || lipgloss.Height(base.Content) != lipgloss.Height(popup.Content) {
					t.Fatalf("popup resized the frame: base=%dx%d popup=%dx%d", lipgloss.Width(base.Content), lipgloss.Height(base.Content), lipgloss.Width(popup.Content), lipgloss.Height(popup.Content))
				}
				baseLines := strings.Split(ansi.Strip(base.Content), "\n")
				popupLines := strings.Split(ansi.Strip(popup.Content), "\n")
				for row := popup.Cursor.Position.Y; row < popup.Cursor.Position.Y+lipgloss.Height(m.input.View()); row++ {
					if baseLines[row] != popupLines[row] {
						t.Fatalf("popup covered input at row %d:\n%s", row, ansi.Strip(popup.Content))
					}
				}
				if !strings.Contains(popupLines[popup.Cursor.Position.Y], "│ /m") || !strings.Contains(ansi.Strip(popup.Content), "› /model") {
					t.Fatalf("popup or input cursor is misplaced:\n%s", ansi.Strip(popup.Content))
				}
			})
		}
	}
}

func TestSlashCompletionPopupIsBoundedAndSharesHelpCatalog(t *testing.T) {
	m := newSlashCompletionModel(t)
	m.input.SetValue("/")
	for _, size := range [][2]int{{12, 3}, {32, 4}, {50, 7}, {100, 20}} {
		popup := m.renderSlashCompletion(size[0], size[1])
		if popup == "" || lipgloss.Width(popup) > size[0] || lipgloss.Height(popup) > min(9, size[1]) {
			t.Fatalf("popup exceeded available %dx%d: %dx%d", size[0], size[1], lipgloss.Width(popup), lipgloss.Height(popup))
		}
	}
	help := ansi.Strip(renderHelpContent(m.dark))
	seen := make(map[string]bool)
	for _, command := range localSlashCommands {
		if seen[command.name] || !strings.Contains(help, command.usage()) {
			t.Fatalf("duplicate or undocumented completion: %q", command.name)
		}
		seen[command.name] = true
	}
}

func TestSlashCompletionPreservesQuestionAndToolShortcuts(t *testing.T) {
	m := newSlashCompletionModel(t)
	m.input.SetValue("/")
	m, _ = completionKey(m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if !m.toolResultsCollapsed || len(m.slashCompletionMatches()) == 0 {
		t.Fatal("completion intercepted Ctrl+O or closed the popup")
	}
	m.waiting, m.asking = true, true
	m.input.Reset()
	m.pendingQuestion = askToUserInput{Question: "Choose", Choices: []askToUserChoice{
		{ID: "one", Label: "One"}, {ID: "two", Label: "Two"},
	}}
	m, _ = completionKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.questionChoice != 1 || m.input.Value() != "" {
		t.Fatal("completion intercepted question choice navigation")
	}
	m.input.SetValue("/mo")
	m, _ = completionKey(m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	if m.input.Value() != "/mod" || len(m.slashCompletionMatches()) != 0 {
		t.Fatal("completion changed a slash-prefixed free-text answer")
	}
}

func TestSlashCompletionOverlayWithAgentTrace(t *testing.T) {
	for _, expanded := range []bool{false, true} {
		t.Run(fmt.Sprintf("expanded=%t", expanded), func(t *testing.T) {
			m := newSlashCompletionModel(t)
			m.agentTraces = []agentTrace{{Agent: "coder", Kind: "tool_result", Name: "search", Content: "trace detail"}}
			m.agentTraceExpanded = expanded
			m.resize(100, 30)
			m.input.SetValue("/")
			view := m.View()
			rows := strings.Split(ansi.Strip(view.Content), "\n")
			if view.Cursor == nil || !strings.Contains(rows[view.Cursor.Position.Y], "│ /") || !strings.Contains(view.Content, "Commands") {
				t.Fatalf("popup misplaced input below the agent trace:\n%s", ansi.Strip(view.Content))
			}
		})
	}
}
