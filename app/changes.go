package app

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/changes"
)

type changesViewState struct {
	snapshot       changes.Snapshot
	cursor         int
	diffFocused    bool
	loading        bool
	previewLoading bool
	err            string
	previewErr     string
	request        uint64
	previewRequest uint64
	cancel         context.CancelFunc
	previewCancel  context.CancelFunc
	viewport       viewport.Model
	preview        changePreview
	cache          map[string]changePreview
}

type changesLoadedMsg struct {
	request, generation uint64
	snapshot            changes.Snapshot
	err                 error
}

type changePreviewMsg struct {
	request, generation uint64
	path                string
	preview             changePreview
	err                 error
}

func newChangesViewState() changesViewState {
	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(12))
	vp.FillHeight = true
	// Do not wrap code: the diff pane supports horizontal scrolling.
	return changesViewState{viewport: vp}
}

func (m model) enterChanges() (tea.Model, tea.Cmd) {
	if m.workspaceStore == nil {
		m.status = "Changes require a workspace directory"
		return m, m.input.Focus()
	}
	m.screen = screenChanges
	m.input.Reset()
	m.input.Blur()
	m.changes.diffFocused = false
	m.status = ""
	command := m.reloadChanges()
	m.resize(m.width, m.height)
	return m, command
}

func (m *model) cancelChanges() {
	if m.changes.cancel != nil {
		m.changes.cancel()
		m.changes.cancel = nil
	}
	if m.changes.previewCancel != nil {
		m.changes.previewCancel()
		m.changes.previewCancel = nil
	}
	// Ignore in-flight replies after refresh, exit, or re-entry.
	m.changes.request++
	m.changes.previewRequest++
}

func (m *model) reloadChanges() tea.Cmd {
	m.cancelChanges()
	m.changes.loading = true
	m.changes.previewLoading = false
	m.changes.err, m.changes.previewErr = "", ""
	m.changes.cache = make(map[string]changePreview)
	m.changes.preview = changePreview{}
	m.refreshChangePreview(true)
	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
	m.changes.cancel = cancel
	request, generation := m.changes.request, m.sessionGeneration
	directory := m.workspaceStore.Root
	return func() tea.Msg {
		defer cancel()
		snapshot, err := changes.List(ctx, directory)
		return changesLoadedMsg{request: request, generation: generation, snapshot: snapshot, err: err}
	}
}

func (m model) receiveChanges(message changesLoadedMsg) (tea.Model, tea.Cmd) {
	if message.request != m.changes.request || message.generation != m.sessionGeneration {
		return m, nil
	}
	m.changes.loading = false
	selected := m.selectedChangePath()
	m.changes.snapshot = message.snapshot
	m.changes.cursor = 0
	if message.err != nil {
		m.changes.err = message.err.Error()
		m.refreshChangePreview(true)
		return m, nil
	}
	for index, file := range message.snapshot.Files {
		if file.Path == selected {
			m.changes.cursor = index
			break
		}
	}
	command := m.loadChangePreview()
	return m, command
}

func (m model) selectedChangePath() string {
	if m.changes.cursor < 0 || m.changes.cursor >= len(m.changes.snapshot.Files) {
		return ""
	}
	return m.changes.snapshot.Files[m.changes.cursor].Path
}

func (m *model) loadChangePreview() tea.Cmd {
	if m.changes.previewCancel != nil {
		m.changes.previewCancel()
	}
	m.changes.previewRequest++
	m.changes.previewLoading = false
	m.changes.previewErr = ""
	m.changes.preview = changePreview{}
	path := m.selectedChangePath()
	if cached, ok := m.changes.cache[path]; ok {
		m.changes.preview = cached
		m.refreshChangePreview(true)
		return nil
	}
	if path == "" {
		m.refreshChangePreview(true)
		return nil
	}
	m.changes.previewLoading = true
	m.refreshChangePreview(true)
	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
	m.changes.previewCancel = cancel
	request, generation := m.changes.previewRequest, m.sessionGeneration
	root, file := m.changes.snapshot.Root, m.changes.snapshot.Files[m.changes.cursor]
	return func() tea.Msg {
		defer cancel()
		detail, err := changes.Read(ctx, root, file)
		message := changePreviewMsg{request: request, generation: generation, path: file.Path, err: err}
		if err == nil && ctx.Err() == nil {
			// Tokenisation happens off the UI loop. Resizing and changing the
			// terminal theme only re-render the already tokenised rows.
			message.preview = buildChangePreview(file.Path, detail)
		}
		if ctx.Err() != nil {
			message.err = ctx.Err()
		}
		return message
	}
}

func (m model) receiveChangePreview(message changePreviewMsg) (tea.Model, tea.Cmd) {
	if message.request != m.changes.previewRequest || message.generation != m.sessionGeneration || message.path != m.selectedChangePath() {
		return m, nil
	}
	m.changes.previewLoading = false
	if message.err != nil {
		m.changes.previewErr = message.err.Error()
	} else {
		m.changes.preview = message.preview
		// Keep browsing memory bounded independently of the repository size.
		if len(m.changes.cache) >= 8 || m.changes.cache == nil {
			m.changes.cache = make(map[string]changePreview)
		}
		m.changes.cache[message.path] = message.preview
	}
	m.refreshChangePreview(true)
	return m, nil
}

func (m model) updateChanges(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.cancelChanges()
		m.screen = screenChat
		m.status = ""
		m.resize(m.width, m.height)
		return m, m.input.Focus()
	case "r":
		command := m.reloadChanges()
		return m, command
	case "tab", "shift+tab":
		m.changes.diffFocused = !m.changes.diffFocused
		return m, nil
	case "enter":
		m.changes.diffFocused = true
		return m, nil
	}
	if m.changes.diffFocused {
		switch key.String() {
		case "home":
			m.changes.viewport.GotoTop()
		case "end":
			m.changes.viewport.GotoBottom()
		default:
			var command tea.Cmd
			m.changes.viewport, command = m.changes.viewport.Update(key)
			return m, command
		}
		return m, nil
	}
	if m.changes.loading {
		return m, nil
	}
	before := m.changes.cursor
	switch key.String() {
	case "up", "k":
		m.changes.cursor--
	case "down", "j":
		m.changes.cursor++
	case "pgup":
		m.changes.cursor -= m.changes.viewport.Height()
	case "pgdown":
		m.changes.cursor += m.changes.viewport.Height()
	case "home":
		m.changes.cursor = 0
	case "end":
		m.changes.cursor = len(m.changes.snapshot.Files) - 1
	}
	m.changes.cursor = max(0, min(m.changes.cursor, len(m.changes.snapshot.Files)-1))
	if before != m.changes.cursor {
		command := m.loadChangePreview()
		return m, command
	}
	return m, nil
}

func (m model) changesLayout() (width, listWidth, diffWidth, height int, wide bool) {
	width = max(1, m.width-frameStyle.GetHorizontalFrameSize())
	height = max(1, m.height-frameStyle.GetVerticalFrameSize()-8)
	wide = width >= 84
	listWidth, diffWidth = width, width
	if wide {
		listWidth = min(40, max(28, width/3))
		diffWidth = width - listWidth - 3
	}
	return
}

func (m *model) resizeChanges() {
	_, _, width, height, _ := m.changesLayout()
	m.changes.viewport.SetWidth(width)
	m.changes.viewport.SetHeight(height)
	if m.screen == screenChanges || (m.screen == screenHelp && m.helpReturn == screenChanges) {
		m.refreshChangePreview(false)
	}
}

func (m *model) refreshChangePreview(top bool) {
	content := "No changes in this repository."
	switch {
	case m.changes.loading:
		content = "Reading repository changes…"
	case m.changes.err != "":
		content = safeChangeText(m.changes.err)
	case m.changes.previewLoading:
		content = "Loading diff…"
	case m.changes.previewErr != "":
		content = safeChangeText(m.changes.previewErr)
	case len(m.changes.preview.rows) > 0:
		content = renderChangePreview(m.changes.preview, m.dark, m.changes.viewport.Width())
	case m.selectedChangePath() != "":
		content = "No diff remains. Press r to refresh the file list."
	}
	m.changes.viewport.SetContent(content)
	if top {
		m.changes.viewport.GotoTop()
		m.changes.viewport.SetXOffset(0)
	} else {
		m.changes.viewport.SetYOffset(m.changes.viewport.YOffset())
	}
}

func (m model) viewChanges() string {
	width, listWidth, diffWidth, height, wide := m.changesLayout()
	var body strings.Builder
	line := func(value string) { body.WriteString(fitChangeLine(value, width)); body.WriteByte('\n') }
	line(titleStyle.Render("q · Changes"))
	root := m.changes.snapshot.Root
	if root == "" && m.workspaceStore != nil {
		root = m.workspaceStore.Root
	}
	line(subtleStyle.Render("Repository · " + safeChangeText(root)))
	line(subtleStyle.Render(fmt.Sprintf("%d files · repository · read only", len(m.changes.snapshot.Files))))
	line("")
	listTitle := "FILES · index/worktree"
	diffTitle := safeChangeText(m.selectedChangePath())
	if diffTitle == "" {
		diffTitle = "DIFF"
	}
	if m.changes.preview.hasLineDiff {
		counts := fmt.Sprintf("+%d -%d", m.changes.preview.added, m.changes.preview.removed)
		if m.changes.preview.truncated {
			counts = "≥ " + counts
		}
		diffTitle = counts + " · " + diffTitle
	}
	listStyle, diffStyle := subtleStyle, subtleStyle
	if m.changes.diffFocused {
		diffStyle = activeLabelStyle
	} else {
		listStyle = activeLabelStyle
	}
	if wide {
		line(fitChangeLine(listStyle.Render(listTitle), listWidth) + " │ " + fitChangeLine(diffStyle.Render(diffTitle), diffWidth))
		divider := strings.TrimSuffix(strings.Repeat(" │ \n", height), "\n")
		body.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, m.renderChangedFiles(listWidth, height), divider, m.changes.viewport.View()))
	} else if m.changes.diffFocused || m.changes.err != "" {
		line(diffStyle.Render(diffTitle))
		body.WriteString(m.changes.viewport.View())
	} else {
		line(listStyle.Render(listTitle))
		body.WriteString(m.renderChangedFiles(width, height))
	}
	body.WriteByte('\n')
	line("")
	status := "↑↓ select · Enter diff · index/worktree status"
	if m.changes.diffFocused {
		status = "↑↓/PgUp/PgDn scroll · ←→ pan"
	}
	statusStyle := subtleStyle
	switch {
	case m.changes.loading:
		status = "Reading repository changes…"
	case m.changes.err != "":
		status, statusStyle = m.changes.err, errorStyle
	case m.changes.previewLoading:
		status = "Loading selected diff…"
	case m.changes.previewErr != "":
		status, statusStyle = m.changes.previewErr, errorStyle
	case m.changes.preview.truncated:
		status = "Partial preview · 256 KiB / 4,000 lines per section"
	}
	line(statusStyle.Render(safeChangeText(status)))
	body.WriteString(fitChangeLine(helpStyle.Render("Tab panes · r refresh · Esc chat"), width))
	return frameStyle.Render(body.String())
}

func (m model) renderChangedFiles(width, height int) string {
	lines := make([]string, height)
	start := max(0, m.changes.cursor-height+1)
	for row := range lines {
		index := start + row
		text := ""
		if index < len(m.changes.snapshot.Files) {
			file := m.changes.snapshot.Files[index]
			path := file.Path
			if file.OldPath != "" {
				path = file.OldPath + " → " + path
			}
			marker := "  "
			style := subtleStyle
			if index == m.changes.cursor {
				marker, style = "› ", activeLabelStyle
			}
			text = style.Render(marker + file.Status + " " + safeChangeText(path))
		} else if row == 0 {
			text = "No changed files."
			if m.changes.loading {
				text = "Loading…"
			}
		}
		lines[row] = fitChangeLine(text, width)
	}
	return strings.Join(lines, "\n")
}

func fitChangeLine(value string, width int) string {
	value = ansi.Truncate(value, max(1, width), "…")
	return value + strings.Repeat(" ", max(0, width-ansi.StringWidth(value)))
}

// Paths, patches, and Git errors are untrusted terminal text. Never let their
// escape sequences, control characters, or bidi controls alter the TUI.
func safeChangeText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return '�'
		}
		return r
	}, strings.ReplaceAll(strings.ToValidUTF8(value, "�"), "\t", "    "))
}
