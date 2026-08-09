package commitagent

import (
	"context"
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
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

func newCommitUIModel(ctx context.Context, directory string) commitUIModel {
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
		ctx: workContext, cancel: cancel, directory: directory, events: events,
		logger: newEventProgressLogger(events), phase: commitUILoading, editor: editor,
	}
}

func (model commitUIModel) Init() tea.Cmd {
	return tea.Batch(model.prepare(), waitCommitUIProgress(model.events))
}

func (model commitUIModel) prepare() tea.Cmd {
	ctx, directory, logger := model.ctx, model.directory, model.logger
	return func() tea.Msg {
		session, err := prepareSessionDefault(ctx, directory, logger)
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
	body.WriteString("q · commit\n")
	if model.directory != "" {
		body.WriteString(model.directory)
		body.WriteString("\n")
	}
	body.WriteString("\n")
	model.renderLogs(&body)
	switch model.phase {
	case commitUILoading:
		body.WriteString("\nPreparing commit proposal…\n\nctrl+c cancel")
	case commitUIReview:
		model.renderReview(&body)
	case commitUIEditing:
		body.WriteString("\nEdit commit message\n\n")
		body.WriteString(model.editor.View())
		if model.status != "" {
			body.WriteString("\n\n")
			body.WriteString(model.status)
		}
		body.WriteString("\n\nctrl+s save · esc cancel")
	case commitUIWorking:
		body.WriteString("\n")
		body.WriteString(model.status)
		body.WriteString("\n\nctrl+c cancel")
	case commitUIDone:
		body.WriteString("\n")
		body.WriteString(model.status)
		body.WriteString("\n")
		for _, message := range model.result.Messages {
			body.WriteString("\n")
			body.WriteString(message)
			body.WriteString("\n")
		}
		if model.pushed {
			body.WriteString("\nenter close")
		} else {
			body.WriteString("\np push · enter close")
		}
	case commitUIFailed:
		body.WriteString("\nFailed\n\n")
		body.WriteString(model.err.Error())
		body.WriteString("\n\nenter close")
	}
	view := tea.NewView(body.String())
	view.AltScreen = true
	view.WindowTitle = "q commit"
	if model.phase == commitUIEditing {
		view.Cursor = model.editor.Cursor()
	}
	return view
}

func (model commitUIModel) renderLogs(body *strings.Builder) {
	visible := 10
	if model.height > 0 {
		visible = max(4, min(14, model.height/3))
	}
	start := max(0, len(model.logs)-visible)
	for _, event := range model.logs[start:] {
		fmt.Fprintf(body, "· %-8s %s\n", event.Stage, event.Message)
	}
}

func (model commitUIModel) renderReview(body *strings.Builder) {
	if len(model.proposals) > 1 {
		fmt.Fprintf(body, "\nSplit proposal · %d commits", len(model.proposals))
	} else {
		body.WriteString("\nReview proposal")
	}
	body.WriteString("\n")
	for index, proposal := range model.proposals {
		prefix := "  "
		if index == model.selected {
			prefix = "› "
		}
		fmt.Fprintf(body, "\n%s%d. %s\n", prefix, index+1, formatSubject(proposal))
		for _, line := range proposal.Body {
			fmt.Fprintf(body, "     - %s\n", line)
		}
		if len(model.proposals) > 1 {
			fmt.Fprintf(body, "     files · %s\n", compactFiles(proposal.Files, 5))
		}
	}
	if model.status != "" {
		body.WriteString("\n")
		body.WriteString(model.status)
		body.WriteString("\n")
	}
	body.WriteString("\n↑/↓ select · e edit message · r regenerate · enter commit · p commit+push · esc cancel")
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
	initial := newCommitUIModel(ctx, directory)
	options := []tea.ProgramOption{tea.WithContext(ctx)}
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
