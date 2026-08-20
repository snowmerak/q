package commitagent

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

var (
	commitTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	commitSubtleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	commitHelpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	commitErrorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	commitActiveStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	commitFrameStyle  = lipgloss.NewStyle().Padding(1, 2)
)

type commitUIPhase int

const (
	commitUILoading commitUIPhase = iota
	commitUIReview
	commitUIEditing
	commitUIWorking
	commitUIDone
	commitUIFailed
)

type commitUIModel struct {
	ctx       context.Context
	cancel    context.CancelFunc
	directory string
	config    *config.Config
	events    chan ProgressEvent
	logger    *progressLogger
	session   *Session
	phase     commitUIPhase
	logs      []ProgressEvent
	proposals []Proposal
	selected  int
	editor    textarea.Model
	status    string
	err       error
	result    Result
	pushed    bool
	width     int
	height    int
}

type commitUIPreparedMsg struct {
	session *Session
	err     error
}

type commitUIRegeneratedMsg struct{ err error }

type commitUIFinishedMsg struct {
	result  Result
	pushed  bool
	pushErr error
	err     error
}

type commitUIPushMsg struct{ err error }

type commitUIProgressMsg ProgressEvent

func newCommitUIModel(ctx context.Context, directory string, value *config.Config) commitUIModel {
	workContext, cancel := context.WithCancel(ctx)
	events := make(chan ProgressEvent, 256)
	editor := textarea.New()
	editor.SetVirtualCursor(false)
	editor.Prompt = "│ "
	editor.Placeholder = "type(scope): summary\n\n- Body item."
	editor.ShowLineNumbers = false
	editor.CharLimit = 16_000
	editor.SetHeight(8)
	editor.SetWidth(76)
	return commitUIModel{
		ctx: workContext, cancel: cancel, directory: directory, config: value, events: events,
		logger: newEventProgressLogger(events), phase: commitUILoading, editor: editor,
	}
}

func (model commitUIModel) Init() tea.Cmd {
	return tea.Batch(model.prepare(), waitCommitUIProgress(model.events))
}

func (model commitUIModel) prepare() tea.Cmd {
	ctx, directory, logger, value := model.ctx, model.directory, model.logger, model.config
	return func() tea.Msg {
		var session *Session
		var err error
		if value == nil {
			session, err = prepareSessionDefault(ctx, directory, logger)
		} else {
			session, err = prepareSessionWithConfig(ctx, directory, *value, logger)
		}
		return commitUIPreparedMsg{session: session, err: err}
	}
}

func (model commitUIModel) regenerate() tea.Cmd {
	ctx, session, logger := model.ctx, model.session, model.logger
	return func() tea.Msg { return commitUIRegeneratedMsg{err: session.Regenerate(ctx, logger)} }
}

func (model commitUIModel) commit(push bool) tea.Cmd {
	ctx, session, logger := model.ctx, model.session, model.logger
	return func() tea.Msg {
		result, err := session.Commit(ctx, logger)
		if err != nil {
			return commitUIFinishedMsg{err: err}
		}
		if push {
			pushErr := session.Push(ctx, logger)
			return commitUIFinishedMsg{result: result, pushed: pushErr == nil, pushErr: pushErr}
		}
		return commitUIFinishedMsg{result: result}
	}
}

func (model commitUIModel) push() tea.Cmd {
	ctx, session, logger := model.ctx, model.session, model.logger
	return func() tea.Msg { return commitUIPushMsg{err: session.Push(ctx, logger)} }
}

func waitCommitUIProgress(events <-chan ProgressEvent) tea.Cmd {
	return func() tea.Msg { return commitUIProgressMsg(<-events) }
}

func (model commitUIModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		model.width, model.height = message.Width, message.Height
		model.editor.SetWidth(max(36, min(100, message.Width-8)))
		model.editor.SetHeight(max(6, min(14, message.Height-12)))
		return model, nil
	case commitUIProgressMsg:
		model.logs = append(model.logs, ProgressEvent(message))
		if len(model.logs) > 200 {
			model.logs = append([]ProgressEvent(nil), model.logs[len(model.logs)-200:]...)
		}
		return model, waitCommitUIProgress(model.events)
	case commitUIPreparedMsg:
		if message.err != nil {
			model.phase, model.err = commitUIFailed, message.err
			return model, nil
		}
		model.session = message.session
		model.proposals = message.session.Proposals()
		model.phase = commitUIReview
		return model, nil
	case commitUIRegeneratedMsg:
		if message.err != nil {
			model.phase, model.status = commitUIReview, message.err.Error()
			return model, nil
		}
		model.proposals = model.session.Proposals()
		model.selected = min(model.selected, max(0, len(model.proposals)-1))
		model.phase, model.status = commitUIReview, "Proposal regenerated"
		return model, nil
	case commitUIFinishedMsg:
		if message.err != nil {
			model.phase, model.status = commitUIReview, message.err.Error()
			return model, nil
		}
		model.result, model.pushed = message.result, message.pushed
		model.phase = commitUIDone
		if message.pushErr != nil {
			model.status = "Commit created, but push failed: " + message.pushErr.Error()
		} else if message.pushed {
			model.status = "Commit created and pushed"
		} else {
			model.status = "Commit created"
		}
		return model, nil
	case commitUIPushMsg:
		if message.err != nil {
			model.status = "Push failed: " + message.err.Error()
		} else {
			model.pushed = true
			model.status = "Push completed"
		}
		return model, nil
	}

	key, ok := message.(tea.KeyPressMsg)
	if !ok {
		return model, nil
	}
	if key.String() == "ctrl+c" {
		model.cancel()
		return model, tea.Quit
	}
	switch model.phase {
	case commitUIReview:
		return model.updateReview(key)
	case commitUIEditing:
		return model.updateEditor(key)
	case commitUIDone:
		switch key.String() {
		case "p":
			if !model.pushed {
				model.status = "Pushing…"
				return model, model.push()
			}
		case "enter", "esc", "q":
			return model, tea.Quit
		}
	case commitUIFailed:
		if key.String() == "enter" || key.String() == "esc" || key.String() == "q" {
			return model, tea.Quit
		}
	}
	return model, nil
}

func (model commitUIModel) updateReview(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "up", "k":
		model.selected = max(0, model.selected-1)
	case "down", "j":
		model.selected = min(max(0, len(model.proposals)-1), model.selected+1)
	case "e":
		if len(model.proposals) > 0 {
			model.editor.SetValue(formatCommitMessage(model.proposals[model.selected]))
			model.phase, model.status = commitUIEditing, ""
			return model, model.editor.Focus()
		}
	case "r":
		model.phase, model.status = commitUIWorking, "Regenerating proposal…"
		return model, model.regenerate()
	case "enter":
		model.phase, model.status = commitUIWorking, "Creating commit…"
		return model, model.commit(false)
	case "p":
		model.phase, model.status = commitUIWorking, "Creating commit and pushing…"
		return model, model.commit(true)
	case "esc", "q":
		model.cancel()
		return model, tea.Quit
	}
	return model, nil
}

func (model commitUIModel) updateEditor(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		model.phase, model.status = commitUIReview, "Edit cancelled"
		model.editor.Blur()
		return model, nil
	case "ctrl+s":
		if err := model.session.UpdateProposal(model.selected, model.editor.Value()); err != nil {
			model.status = err.Error()
			return model, nil
		}
		model.proposals = model.session.Proposals()
		model.phase, model.status = commitUIReview, "Message updated"
		model.editor.Blur()
		return model, nil
	}
	var command tea.Cmd
	model.editor, command = model.editor.Update(key)
	return model, command
}

func (model commitUIModel) View() tea.View {
	var body strings.Builder
	body.WriteString(commitTitleStyle.Render("q · commit"))
	body.WriteString("\n")
	workspace := model.directory
	if model.session != nil {
		workspace = model.session.Root()
	}
	if workspace != "" {
		body.WriteString(commitSubtleStyle.Render("workspace · " + filepath.Clean(workspace)))
		body.WriteString("\n")
	}
	body.WriteString("\n")
	model.renderLogs(&body)
	editorOffsetY := 0
	switch model.phase {
	case commitUILoading:
		body.WriteString("\n")
		body.WriteString(commitSubtleStyle.Render("Preparing commit proposal…"))
		body.WriteString("\n\n")
		body.WriteString(commitHelpStyle.Render("ctrl+c cancel"))
	case commitUIReview:
		model.renderReview(&body)
	case commitUIEditing:
		body.WriteString("\n")
		body.WriteString(commitActiveStyle.Render("Edit commit message"))
		body.WriteString("\n\n")
		editorOffsetY = lipgloss.Height(body.String())
		body.WriteString(model.editor.View())
		if model.status != "" {
			body.WriteString("\n\n")
			body.WriteString(commitErrorStyle.Render(model.status))
		}
		body.WriteString("\n\n")
		body.WriteString(commitHelpStyle.Render("ctrl+s save · esc cancel"))
	case commitUIWorking:
		body.WriteString("\n")
		body.WriteString(commitActiveStyle.Render("› "))
		body.WriteString(commitSubtleStyle.Render(model.status))
		body.WriteString("\n\n")
		body.WriteString(commitHelpStyle.Render("ctrl+c cancel"))
	case commitUIDone:
		body.WriteString("\n")
		body.WriteString(commitActiveStyle.Render("✓ " + model.status))
		body.WriteString("\n")
		for _, message := range model.result.Messages {
			body.WriteString("\n")
			body.WriteString(commitSubtleStyle.Render(message))
			body.WriteString("\n")
		}
		if model.pushed {
			body.WriteString("\n")
			body.WriteString(commitHelpStyle.Render("enter close"))
		} else {
			body.WriteString("\n")
			body.WriteString(commitHelpStyle.Render("p push · enter close"))
		}
	case commitUIFailed:
		body.WriteString("\n")
		body.WriteString(commitErrorStyle.Render("Failed"))
		body.WriteString("\n\n")
		body.WriteString(commitErrorStyle.Render(model.err.Error()))
		body.WriteString("\n\n")
		body.WriteString(commitHelpStyle.Render("enter close"))
	}
	frameWidth := model.width - 4
	if model.width <= 0 {
		frameWidth = 80
	}
	content := commitFrameStyle.Width(max(36, frameWidth)).Render(body.String())
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "q commit"
	if model.phase == commitUIEditing {
		if cursor := model.editor.Cursor(); cursor != nil {
			cursor.Position.X += commitFrameStyle.GetPaddingLeft()
			cursor.Position.Y += commitFrameStyle.GetPaddingTop() + editorOffsetY
			view.Cursor = cursor
		}
	}
	return view
}

func (model commitUIModel) renderLogs(body *strings.Builder) {
	visible := 10
	if model.height > 0 {
		visible = max(4, min(14, model.height/3))
	}
	start := max(0, len(model.logs)-visible)
	for index, event := range model.logs[start:] {
		prefix := commitSubtleStyle.Render("· ")
		stageStyle := commitSubtleStyle
		if start+index == len(model.logs)-1 && (model.phase == commitUILoading || model.phase == commitUIWorking) {
			prefix = commitActiveStyle.Render("› ")
			stageStyle = commitActiveStyle
		}
		body.WriteString(prefix)
		body.WriteString(stageStyle.Render(fmt.Sprintf("%-8s", event.Stage)))
		body.WriteString(" ")
		body.WriteString(commitSubtleStyle.Render(event.Message))
		body.WriteString("\n")
	}
}

func (model commitUIModel) renderReview(body *strings.Builder) {
	if len(model.proposals) > 1 {
		body.WriteString("\n")
		body.WriteString(commitSubtleStyle.Render(fmt.Sprintf("Split proposal · %d commits", len(model.proposals))))
	} else {
		body.WriteString("\n")
		body.WriteString(commitSubtleStyle.Render("Review proposal"))
	}
	body.WriteString("\n")
	for index, proposal := range model.proposals {
		prefix := "  "
		style := commitSubtleStyle
		if index == model.selected {
			prefix = "› "
			style = commitActiveStyle
		}
		body.WriteString("\n")
		body.WriteString(prefix)
		body.WriteString(style.Render(fmt.Sprintf("%d. %s", index+1, formatSubject(proposal))))
		body.WriteString("\n")
		for _, line := range proposal.Body {
			body.WriteString(commitSubtleStyle.Render("     - " + line))
			body.WriteString("\n")
		}
		if len(model.proposals) > 1 {
			body.WriteString(commitSubtleStyle.Render("     files · " + compactFiles(proposal.Files, 5)))
			body.WriteString("\n")
		}
	}
	if model.status != "" {
		body.WriteString("\n")
		body.WriteString(commitSubtleStyle.Render(model.status))
		body.WriteString("\n")
	}
	body.WriteString("\n")
	body.WriteString(commitHelpStyle.Render("↑/↓ select · e edit message · r regenerate · enter commit · p commit+push · esc cancel"))
}

func compactFiles(files []string, limit int) string {
	if len(files) == 0 {
		return "none"
	}
	if len(files) <= limit {
		return strings.Join(files, ", ")
	}
	return strings.Join(files[:limit], ", ") + fmt.Sprintf(" +%d", len(files)-limit)
}

// RunDefault opens the interactive commit review UI.
func RunDefault(ctx context.Context, directory string, output io.Writer) (Result, error) {
	return RunEmbedded(ctx, directory, nil, output, nil)
}

// RunEmbedded opens the interactive commit review UI inside another terminal
// application. It reuses existingLock when it owns the Git root, or acquires a
// supplemental repository-root lock when the embedding q was launched from a
// subdirectory. The caller always retains ownership of existingLock.
func RunEmbedded(
	ctx context.Context,
	directory string,
	input io.Reader,
	output io.Writer,
	existingLock *workspace.Lock,
) (Result, error) {
	return runEmbedded(ctx, directory, input, output, existingLock, nil)
}

// RunEmbeddedWithConfig opens the commit UI with the caller's effective
// interactive configuration. This lets an embedding q process preserve a
// workspace model override without changing global configuration.
func RunEmbeddedWithConfig(
	ctx context.Context,
	directory string,
	input io.Reader,
	output io.Writer,
	existingLock *workspace.Lock,
	value config.Config,
) (Result, error) {
	return runEmbedded(ctx, directory, input, output, existingLock, &value)
}

func runEmbedded(
	ctx context.Context,
	directory string,
	input io.Reader,
	output io.Writer,
	existingLock *workspace.Lock,
	value *config.Config,
) (Result, error) {
	root, err := repositoryRoot(ctx, directory)
	if err != nil {
		return Result{}, err
	}
	if existingLock == nil || !existingLock.Owns(root) {
		repositoryLock, lockErr := workspace.AcquireLock(root, "q commit")
		if lockErr != nil {
			return Result{}, lockErr
		}
		defer repositoryLock.Close()
	}
	return runCommitUI(ctx, directory, input, output, value)
}

func runCommitUI(ctx context.Context, directory string, input io.Reader, output io.Writer, value *config.Config) (Result, error) {
	initial := newCommitUIModel(ctx, directory, value)
	options := []tea.ProgramOption{tea.WithContext(ctx)}
	if input != nil {
		options = append(options, tea.WithInput(input))
	}
	if output != nil {
		options = append(options, tea.WithOutput(output))
	}
	final, err := tea.NewProgram(initial, options...).Run()
	model, ok := final.(commitUIModel)
	if ok {
		model.cancel()
		if model.session != nil {
			closeErr := model.session.Close()
			if err == nil {
				err = closeErr
			}
		}
		if model.phase == commitUIFailed && model.err != nil && err == nil {
			err = model.err
		}
		return model.result, err
	}
	initial.cancel()
	return Result{}, err
}
