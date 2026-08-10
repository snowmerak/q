package app

import (
	"context"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/commitagent"
	"github.com/snowmerak/q/workspace"
)

type commitFinishedMsg struct {
	result commitagent.Result
	err    error
}

type embeddedCommitCommand struct {
	ctx       context.Context
	directory string
	lock      *workspace.Lock
	input     io.Reader
	output    io.Writer
	stderr    io.Writer
	result    commitagent.Result
}

var _ tea.ExecCommand = (*embeddedCommitCommand)(nil)

func (command *embeddedCommitCommand) SetStdin(input io.Reader)   { command.input = input }
func (command *embeddedCommitCommand) SetStdout(output io.Writer) { command.output = output }
func (command *embeddedCommitCommand) SetStderr(stderr io.Writer) { command.stderr = stderr }

func (command *embeddedCommitCommand) Run() error {
	result, err := commitagent.RunEmbedded(
		command.ctx, command.directory, command.input, command.output, command.lock,
	)
	command.result = result
	return err
}

func (m model) startCommit() (tea.Model, tea.Cmd) {
	if m.workspaceStore == nil || m.workspaceLock == nil {
		m.status = "Commit UI is unavailable without workspace ownership"
		return m, m.input.Focus()
	}
	m.input.Reset()
	m.input.Blur()
	m.commitRunning = true
	m.status = "Opening commit workflow…"
	command := &embeddedCommitCommand{
		ctx: m.ctx, directory: m.workspaceStore.Root, lock: m.workspaceLock,
	}
	return m, tea.Exec(command, func(err error) tea.Msg {
		return commitFinishedMsg{result: command.result, err: err}
	})
}

func commitResultStatus(result commitagent.Result) string {
	if len(result.Messages) == 0 {
		return "Commit cancelled"
	}
	subjects := make([]string, 0, len(result.Messages))
	for _, message := range result.Messages {
		subject := strings.TrimSpace(strings.SplitN(message, "\n", 2)[0])
		if subject != "" {
			subjects = append(subjects, subject)
		}
	}
	if len(subjects) == 0 {
		return "Commit completed"
	}
	if len(subjects) == 1 {
		return "Committed · " + subjects[0]
	}
	return "Committed " + strings.Join(subjects, " · ")
}
