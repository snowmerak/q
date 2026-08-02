package builtin

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

const (
	commandOutputLimit = 4 << 20
	commandReadLimit   = 256 << 10
	maximumWait        = 60 * time.Second
)

type RunCommandInput struct {
	Command string `json:"command" jsonschema:"Shell command to run."`
	Workdir string `json:"workdir,omitempty" jsonschema:"Starting directory inside the workspace. Defaults to the workspace root."`
}

type CommandInput struct {
	CommandID string `json:"command_id" jsonschema:"Identifier returned by run_command."`
	Offset    int64  `json:"offset,omitempty" jsonschema:"Output byte offset returned by a previous call. Defaults to zero."`
}

type WaitInput struct {
	CommandID string `json:"command_id" jsonschema:"Identifier returned by run_command."`
	Offset    int64  `json:"offset,omitempty" jsonschema:"Output byte offset returned by a previous call. Defaults to zero."`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"Maximum wait in milliseconds. Defaults to 30000 and is capped at 60000."`
}

type CommandOutput struct {
	CommandID  string     `json:"command_id"`
	Status     string     `json:"status"`
	PID        int        `json:"pid"`
	Workdir    string     `json:"workdir"`
	Output     string     `json:"output,omitempty"`
	NextOffset int64      `json:"next_offset"`
	MoreOutput bool       `json:"more_output,omitempty"`
	Truncated  bool       `json:"truncated,omitempty"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type commandRegistry struct {
	root     string
	mu       sync.Mutex
	nextID   uint64
	commands map[string]*commandState
}

type commandState struct {
	id      string
	command *exec.Cmd
	workdir string
	started time.Time
	done    chan struct{}
	output  commandBuffer

	mu       sync.RWMutex
	finished *time.Time
	exitCode *int
}

type commandBuffer struct {
	mu    sync.Mutex
	base  int64
	data  []byte
	limit int
}

func newCommandRegistry(root string) *commandRegistry {
	return &commandRegistry{root: root, commands: make(map[string]*commandState)}
}

func (fs *FS) RunCommand(input RunCommandInput) (CommandOutput, error) {
	return fs.commands.Run(input, func(path string) (string, error) {
		resolved, err := fs.resolveExisting(path)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("[E_PATH] command workdir is not a directory")
		}
		return resolved, nil
	})
}

func (fs *FS) CommandStatus(input CommandInput) (CommandOutput, error) {
	return fs.commands.Status(input)
}

func (fs *FS) WaitCommand(input WaitInput) (CommandOutput, error) {
	return fs.commands.Wait(input)
}

func (r *commandRegistry) Run(input RunCommandInput, resolveWorkdir func(string) (string, error)) (CommandOutput, error) {
	if input.Command == "" {
		return CommandOutput{}, fmt.Errorf("[E_COMMAND] command is required")
	}
	userWorkdir := input.Workdir
	if userWorkdir == "" {
		userWorkdir = "."
	}
	workdir, err := resolveWorkdir(userWorkdir)
	if err != nil {
		return CommandOutput{}, err
	}

	command := shellCommand(input.Command)
	command.Dir = workdir
	r.mu.Lock()
	r.nextID++
	id := fmt.Sprintf("cmd-%d", r.nextID)
	state := &commandState{
		id: id, command: command, workdir: workdir, started: time.Now(), done: make(chan struct{}),
		output: commandBuffer{limit: commandOutputLimit},
	}
	command.Stdout = &state.output
	command.Stderr = &state.output
	if err := command.Start(); err != nil {
		r.mu.Unlock()
		return CommandOutput{}, fmt.Errorf("[E_COMMAND] start: %w", err)
	}
	r.commands[id] = state
	r.mu.Unlock()

	go state.collect()
	return state.snapshot(0), nil
}

func (r *commandRegistry) Status(input CommandInput) (CommandOutput, error) {
	state, err := r.lookup(input.CommandID)
	if err != nil {
		return CommandOutput{}, err
	}
	return state.snapshot(input.Offset), nil
}

func (r *commandRegistry) Wait(input WaitInput) (CommandOutput, error) {
	state, err := r.lookup(input.CommandID)
	if err != nil {
		return CommandOutput{}, err
	}
	timeout := 30 * time.Second
	if input.TimeoutMS > 0 {
		timeout = time.Duration(input.TimeoutMS) * time.Millisecond
	}
	if timeout > maximumWait {
		timeout = maximumWait
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-state.done:
	case <-timer.C:
	}
	return state.snapshot(input.Offset), nil
}

func (r *commandRegistry) Close() {
	r.mu.Lock()
	states := make([]*commandState, 0, len(r.commands))
	for _, state := range r.commands {
		states = append(states, state)
	}
	r.mu.Unlock()
	for _, state := range states {
		select {
		case <-state.done:
		default:
			terminateCommand(state.command)
		}
	}
}

func (r *commandRegistry) lookup(id string) (*commandState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.commands[id]
	if state == nil {
		return nil, fmt.Errorf("[E_COMMAND] unknown command_id %q", id)
	}
	return state, nil
}

func (s *commandState) collect() {
	err := s.command.Wait()
	exitCode := 0
	if err != nil {
		exitCode = -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	finished := time.Now()
	s.mu.Lock()
	s.exitCode = &exitCode
	s.finished = &finished
	s.mu.Unlock()
	close(s.done)
}

func (s *commandState) snapshot(offset int64) CommandOutput {
	output, next, more, truncated := s.output.read(offset, commandReadLimit)
	s.mu.RLock()
	result := CommandOutput{
		CommandID: s.id, Status: "running", PID: s.command.Process.Pid, Workdir: s.workdir,
		Output: output, NextOffset: next, MoreOutput: more, Truncated: truncated,
		ExitCode: s.exitCode, StartedAt: s.started, FinishedAt: s.finished,
	}
	if s.exitCode != nil {
		if *s.exitCode == 0 {
			result.Status = "succeeded"
		} else {
			result.Status = "failed"
		}
	}
	s.mu.RUnlock()
	return result
}

func (b *commandBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, value...)
	if len(b.data) > b.limit {
		drop := len(b.data) - b.limit
		b.data = append([]byte(nil), b.data[drop:]...)
		b.base += int64(drop)
	}
	return len(value), nil
}

func (b *commandBuffer) read(offset int64, limit int) (string, int64, bool, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	truncated := offset < b.base
	if offset < b.base {
		offset = b.base
	}
	endOffset := b.base + int64(len(b.data))
	if offset > endOffset {
		offset = endOffset
	}
	start := int(offset - b.base)
	end := min(len(b.data), start+limit)
	return string(b.data[start:end]), b.base + int64(end), end < len(b.data), truncated
}
