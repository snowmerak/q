package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/changes"
	"github.com/snowmerak/q/workspace"
)

func TestChangesCommandLoadsRealRepositoryAndReturnsToChat(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable")
	}
	root := t.TempDir()
	if output, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", output, err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nvar greeting = \"hello\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newSlashCompletionModel(t)
	m.workspaceStore = &workspace.Store{Root: root}
	m.resize(120, 28)
	messageCount := len(m.messages)
	m.input.SetValue("/changes")
	updated, command := m.submitChat()
	m = updated.(model)
	t.Cleanup(func() { m.cancelChanges() })
	if m.screen != screenChanges || !m.changes.loading || command == nil || m.input.Focused() || len(m.messages) != messageCount {
		t.Fatal("/changes did not start a local read-only view")
	}
	updated, command = m.Update(command())
	m = updated.(model)
	if m.changes.loading || !m.changes.previewLoading || command == nil || m.selectedChangePath() != "main.go" {
		t.Fatalf("list did not start selected preview: %#v", m.changes)
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"q · Changes", "main.go", "UNTRACKED", "+3 -0", "greeting", "hello", "read only"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "index")); !os.IsNotExist(err) {
		t.Fatalf("browsing created an index: %v", err)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(model)
	if m.screen != screenChat || !m.input.Focused() || len(m.messages) != messageCount {
		t.Fatal("Escape did not restore the unchanged chat")
	}
}

func changesTestModel(t *testing.T) model {
	t.Helper()
	m := newSlashCompletionModel(t)
	m.screen = screenChanges
	m.input.Blur()
	m.workspaceStore = &workspace.Store{Root: t.TempDir()}
	m.changes.snapshot = changes.Snapshot{Root: m.workspaceStore.Root, Files: []changes.File{
		{Path: "a.go", Status: " M"}, {Path: "b.go", Status: "??"},
	}}
	m.resize(120, 24)
	t.Cleanup(func() { m.cancelChanges() })
	return m
}

func TestChangesIgnoresStaleLoadsAndRefreshesCachedPreview(t *testing.T) {
	m := changesTestModel(t)
	m.changes.cache = make(map[string]changePreview)
	_ = m.loadChangePreview()
	firstRequest := m.changes.previewRequest
	updated, command := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(model)
	if m.selectedChangePath() != "b.go" || command == nil || !m.changes.previewLoading {
		t.Fatal("selection did not request the next file")
	}
	updated, _ = m.Update(changePreviewMsg{request: firstRequest, path: "a.go", preview: changePreview{added: 99}})
	m = updated.(model)
	if m.changes.preview.added == 99 || !m.changes.previewLoading {
		t.Fatal("stale preview replaced the selected file")
	}
	preview := buildChangePreview("b.go", changes.Detail{Sections: []changes.Section{{Title: "UNTRACKED", Patch: "@@ -0,0 +1 @@\n+package main\n"}}})
	updated, _ = m.Update(changePreviewMsg{request: m.changes.previewRequest, generation: m.sessionGeneration, path: "b.go", preview: preview})
	m = updated.(model)
	if m.changes.previewLoading || m.changes.preview.added != 1 {
		t.Fatal("current preview not applied")
	}
	if command := m.loadChangePreview(); command != nil || m.changes.preview.added != 1 {
		t.Fatal("cached selection was not reused")
	}
	oldRequest := m.changes.request
	updated, command = m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = updated.(model)
	if command == nil || !m.changes.loading || len(m.changes.cache) != 0 {
		t.Fatal("refresh did not clear the cached snapshot")
	}
	updated, _ = m.Update(changesLoadedMsg{request: oldRequest, err: errors.New("stale error")})
	m = updated.(model)
	if m.changes.err != "" || !m.changes.loading {
		t.Fatal("old list reply was accepted after refresh")
	}
	request := m.changes.request
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(model)
	updated, _ = m.Update(changesLoadedMsg{request: request, err: errors.New("late error")})
	m = updated.(model)
	if m.screen != screenChat || m.changes.err != "" {
		t.Fatal("reply after exit affected chat")
	}
	// Even a matching request number must not cross a session boundary.
	updated, _ = m.Update(changesLoadedMsg{request: m.changes.request, generation: m.sessionGeneration + 1, err: errors.New("wrong session")})
	if updated.(model).changes.err != "" {
		t.Fatal("reply from another session was accepted")
	}
	m.cancelChanges()
}

func TestChangesViewportFocusResizeThemeAndHelp(t *testing.T) {
	m := changesTestModel(t)
	patch := "@@ -1 +1,100 @@\n-old\n" + strings.Repeat("+var answer = \""+strings.Repeat("long ", 35)+"\"\n", 100)
	m.changes.preview = buildChangePreview("a.go", changes.Detail{Sections: []changes.Section{{Title: "UNSTAGED", Patch: patch}}})
	m.refreshChangePreview(true)
	for _, size := range [][2]int{{40, 12}, {60, 18}, {90, 24}, {120, 32}} {
		for _, focused := range []bool{false, true} {
			m.changes.diffFocused = focused
			m.resize(size[0], size[1])
			view := m.View().Content
			if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
				t.Fatalf("view overflow at %v focused=%v: %dx%d\n%s", size, focused, lipgloss.Width(view), lipgloss.Height(view), ansi.Strip(view))
			}
			if !strings.Contains(ansi.Strip(view), "Esc chat") {
				t.Fatal("narrow layout hid the exit key")
			}
		}
	}
	m.changes.diffFocused = false
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = updated.(model)
	if !m.changes.diffFocused {
		t.Fatal("Tab did not switch focus")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = updated.(model)
	if m.changes.viewport.YOffset() == 0 || m.changes.cursor != 0 {
		t.Fatal("diff scrolling changed file selection or failed to scroll")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = updated.(model)
	if m.changes.viewport.XOffset() == 0 {
		t.Fatal("long code lines did not scroll horizontally")
	}
	y := m.changes.viewport.YOffset()
	beforeTheme := m.changes.viewport.View()
	m.applyColorScheme(!m.dark)
	if beforeTheme == m.changes.viewport.View() || m.changes.viewport.YOffset() != y {
		t.Fatal("theme change did not recolour the diff while retaining scroll")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	m = updated.(model)
	if m.screen != screenHelp || m.helpReturn != screenChanges {
		t.Fatal("help did not remember changes view")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(model)
	if m.screen != screenChanges || m.input.Focused() || m.changes.viewport.YOffset() != y {
		t.Fatal("help return lost diff state or focused chat input")
	}
}

func TestChangePreviewNumbersSyntaxAndTerminalSafety(t *testing.T) {
	patch := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -10,3 +10,4 @@\n package main\n-var s = `old\n+var s = `new\n multiline`\n+var answer = \"hello\"\n@@ -90 +91 @@\n-old\n+++actual code beginning with pluses\n"
	preview := buildChangePreview("main.go", changes.Detail{Sections: []changes.Section{{Title: "STAGED", Patch: patch}}})
	if preview.added != 3 || preview.removed != 2 {
		t.Fatalf("counts = +%d -%d", preview.added, preview.removed)
	}
	keyword, literal, numbered := false, false, false
	for _, row := range preview.rows {
		var text strings.Builder
		for _, token := range row.tokens {
			text.WriteString(strings.TrimSuffix(token.Value, "\n"))
			keyword = keyword || strings.HasPrefix(token.Type.String(), "Keyword")
			literal = literal || strings.HasPrefix(token.Type.String(), "LiteralString")
		}
		if len(row.tokens) > 0 && text.String() != row.text {
			t.Fatalf("tokenisation changed source: %q != %q", text.String(), row.text)
		}
		if row.text == "++actual code beginning with pluses" && row.newLine == 91 && row.oldLine == 0 {
			numbered = true
		}
	}
	if !keyword || !literal || !numbered {
		t.Fatalf("syntax or line numbers missing: keyword=%v string=%v numbered=%v", keyword, literal, numbered)
	}
	dark, light := renderChangePreview(preview, true, 80), renderChangePreview(preview, false, 80)
	if dark == light || ansi.Strip(dark) != ansi.Strip(light) || !strings.Contains(dark, "\x1b[") {
		t.Fatal("theme palettes changed source or did not provide highlighting")
	}
	unsafe := "\x1b]52;c;clipboard\a\x1b[2J\r\u202e"
	preview = buildChangePreview("unknown.extension", changes.Detail{Sections: []changes.Section{{Title: "UNTRACKED", Patch: "@@ -0,0 +1 @@\n+" + unsafe + "\n", Truncated: true}}})
	rendered := renderChangePreview(preview, true, 60)
	if strings.Contains(rendered, "\x1b]") || strings.Contains(rendered, "\x1b[2J") || strings.ContainsAny(rendered, "\a\r\u202e") || !strings.Contains(rendered, "partial preview") {
		t.Fatal("untrusted code escaped the terminal text boundary or truncation was hidden")
	}
}

func TestChangesEmptyErrorAndRenameViews(t *testing.T) {
	m := changesTestModel(t)
	m.changes.snapshot.Files = nil
	m.refreshChangePreview(true)
	if !strings.Contains(ansi.Strip(m.View().Content), "No changes in this repository") {
		t.Fatal("empty state was not shown")
	}
	m.changes.snapshot.Files = []changes.File{{Path: "new\nname.go", OldPath: "old.go", Status: "R "}}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "old.go → new�name.go") {
		t.Fatalf("rename not safely rendered:\n%s", view)
	}
	for _, size := range [][2]int{{40, 12}, {120, 24}} {
		m.resize(size[0], size[1])
		updated, _ := m.Update(changesLoadedMsg{request: m.changes.request, generation: m.sessionGeneration, err: fmt.Errorf("not a Git repository")})
		m = updated.(model)
		if !strings.Contains(ansi.Strip(m.View().Content), "not a Git repository") {
			t.Fatal("Git failure was not displayed")
		}
	}
}

func TestChangeSyntaxLanguageDetectionAndPlainFallback(t *testing.T) {
	for _, test := range []struct{ path, source string }{
		{"main.go", "package main\nvar text = \"hello\"\n"},
		{"main.py", "def greet():\n    return \"hello\"\n"},
		{"main.ts", "const greet = () => \"hello\";\n"},
	} {
		t.Run(test.path, func(t *testing.T) {
			lines := changeSyntaxTokens(test.path, test.source)
			keyword, literal := false, false
			for _, line := range lines {
				for _, token := range line {
					keyword = keyword || strings.HasPrefix(token.Type.String(), "Keyword")
					literal = literal || strings.HasPrefix(token.Type.String(), "LiteralString")
				}
			}
			if !keyword || !literal {
				t.Fatalf("language was not highlighted: keyword=%v literal=%v", keyword, literal)
			}
		})
	}
	if tokens := changeSyntaxTokens("file.q-unknown-extension", "plain text\n"); len(tokens) != 0 {
		t.Fatal("unknown file did not fall back to plain text")
	}
	if tokens := changeSyntaxTokens("large.go", strings.Repeat("x", (128<<10)+1)); len(tokens) != 0 {
		t.Fatal("oversized hunk was sent to the lexer")
	}
	m := changesTestModel(t)
	m.changes.preview = buildChangePreview("image.png", changes.Detail{Sections: []changes.Section{{Title: "UNSTAGED", Patch: "Binary files a/image.png and b/image.png differ"}}})
	m.refreshChangePreview(true)
	if strings.Contains(ansi.Strip(m.View().Content), "+0 -0") {
		t.Fatal("binary file displayed misleading line counts")
	}
}
