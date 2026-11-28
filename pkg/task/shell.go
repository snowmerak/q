package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/v2/pkg/domain"
)

const (
	shellBufferCapBytes = 4 << 20
	shellReadChunkBytes = 256 << 10
	shellMaxWaitSec     = 60
)

type shellJob struct {
	id         string
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	mu         sync.Mutex
	buf        bytes.Buffer
	truncated  bool
	readOffset int
	done       bool
	exitCode   int
	errText    string
	doneCh     chan struct{}
}

func (j *shellJob) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	avail := shellBufferCapBytes - j.buf.Len()
	if avail <= 0 {
		j.truncated = true
		return len(p), nil
	}
	if len(p) > avail {
		_, _ = j.buf.Write(p[:avail])
		j.truncated = true
		return len(p), nil
	}
	_, _ = j.buf.Write(p)
	return len(p), nil
}

func (j *shellJob) snapshot(fromOffset int) (chunk string, nextOffset int, done bool, exitCode int, errText string, truncated bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if fromOffset < 0 {
		fromOffset = j.readOffset
	}
	all := j.buf.Bytes()
	if fromOffset > len(all) {
		fromOffset = len(all)
	}
	end := min(len(all), fromOffset+shellReadChunkBytes)
	chunk = string(all[fromOffset:end])
	j.readOffset = end
	return chunk, end, j.done, j.exitCode, j.errText, j.truncated
}

type CommandManager struct {
	defaultTimeout time.Duration
	mu             sync.Mutex
	jobs           map[string]*shellJob
}

func NewCommandManager(defaultTimeout time.Duration) *CommandManager {
	if defaultTimeout <= 0 {
		defaultTimeout = 120 * time.Second
	}
	return &CommandManager{
		defaultTimeout: defaultTimeout,
		jobs:           make(map[string]*shellJob),
	}
}

func (m *CommandManager) Start(ctx context.Context, command, workDir string, timeout time.Duration) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("command is required")
	}
	if timeout <= 0 {
		timeout = m.defaultTimeout
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)

	var cmd *exec.Cmd
	if goruntime.GOOS == "windows" {
		cmd = exec.Command("powershell", "-NoProfile", "-Command", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	if workDir != "" {
		cmd.Dir = workDir
	}
	configureShellCmd(cmd)

	id := domain.NewID()
	job := &shellJob{
		id:     id,
		cmd:    cmd,
		cancel: cancel,
		doneCh: make(chan struct{}),
	}
	cmd.Stdout = job
	cmd.Stderr = job

	if err := cmd.Start(); err != nil {
		cancel()
		return "", err
	}

	m.mu.Lock()
	m.jobs[id] = job
	m.mu.Unlock()

	go func() {
		defer close(job.doneCh)
		defer cancel()

		waitDone := make(chan error, 1)
		go func() {
			waitDone <- cmd.Wait()
		}()

		var waitErr error
		select {
		case waitErr = <-waitDone:
		case <-runCtx.Done():
			killProcessTree(cmd)
			waitErr = <-waitDone
		}

		exitCode := 0
		errText := ""
		if waitErr != nil {
			errText = waitErr.Error()
			var ee *exec.ExitError
			if errors.As(waitErr, &ee) {
				exitCode = ee.ExitCode()
			} else {
				exitCode = -1
			}
		}
		if runCtx.Err() != nil && errText == "" {
			errText = runCtx.Err().Error()
		}

		job.mu.Lock()
		job.done = true
		job.exitCode = exitCode
		job.errText = errText
		job.mu.Unlock()
	}()

	return id, nil
}

func (m *CommandManager) WaitJob(ctx context.Context, id string, waitDur time.Duration) (*shellJob, error) {
	m.mu.Lock()
	job, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown command_id %q", id)
	}
	if waitDur > 0 {
		timer := time.NewTimer(waitDur)
		defer timer.Stop()
		select {
		case <-job.doneCh:
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return job, nil
}

func (m *CommandManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	jobs := make([]*shellJob, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	m.mu.Unlock()
	for _, j := range jobs {
		j.cancel()
		killProcessTree(j.cmd)
		<-j.doneCh
	}
}

func registerShellTools(r *Registry, mgr *CommandManager) {
	r.register(ToolSpec{
		Name:        "run_command",
		Description: "Start an asynchronous shell command and return its command_id.",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskExec,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":    map[string]any{"type": "string"},
				"cwd":        map[string]any{"type": "string", "description": "Optional working directory (defaults to room work_dir or home; restricted to allowed_roots only if configured)."},
				"timeout_ms": map[string]any{"type": "integer"},
			},
			"required": []string{"command"},
		},
		Handler: func(ctx context.Context, call CallContext, argsJSON string) (ToolOutput, error) {
			if mgr == nil {
				return ToolOutput{Content: "shell command manager unavailable", IsError: true}, nil
			}
			var args struct {
				Command   string `json:"command"`
				Cwd       string `json:"cwd"`
				TimeoutMS int    `json:"timeout_ms"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			guard, err := newPathGuard(call.AllowedRoots, call.DataDir, call.WorkDir)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			targetCwd := strings.TrimSpace(args.Cwd)
			if targetCwd == "" {
				targetCwd = guard.defaultBaseDir()
			}
			workDir := ""
			if targetCwd != "" {
				resolved, err := guard.resolve(targetCwd)
				if err != nil {
					return ToolOutput{Content: err.Error(), IsError: true}, nil
				}
				workDir = filepath.Clean(resolved)
			}

			var timeout time.Duration
			if args.TimeoutMS > 0 {
				timeout = time.Duration(args.TimeoutMS) * time.Millisecond
			}
			// Run under background context bounded by timeout so command keeps running across tool rounds,
			// while CommandManager.Close() or explicit stop cancels it.
			id, err := mgr.Start(context.Background(), args.Command, workDir, timeout)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			out, _ := json.Marshal(map[string]any{
				"command_id": id,
				"status":     "running",
			})
			return ToolOutput{Content: string(out)}, nil
		},
	})

	r.register(ToolSpec{
		Name:        "cmd_status",
		Description: "Read incremental buffered output and completion status for a command_id.",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskRead,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command_id": map[string]any{"type": "string"},
				"offset":     map[string]any{"type": "integer", "description": "Optional byte offset (-1 continues from last read)."},
			},
			"required": []string{"command_id"},
		},
		Handler: func(ctx context.Context, _ CallContext, argsJSON string) (ToolOutput, error) {
			if mgr == nil {
				return ToolOutput{Content: "shell command manager unavailable", IsError: true}, nil
			}
			var args struct {
				CommandID string `json:"command_id"`
				Offset    *int   `json:"offset"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			job, err := mgr.WaitJob(ctx, args.CommandID, 0)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			from := -1
			if args.Offset != nil {
				from = *args.Offset
			}
			chunk, nextOff, done, exitCode, errText, truncated := job.snapshot(from)
			out, _ := json.Marshal(map[string]any{
				"command_id":  args.CommandID,
				"done":        done,
				"exit_code":   exitCode,
				"error":       errText,
				"output":      chunk,
				"next_offset": nextOff,
				"truncated":   truncated,
			})
			return ToolOutput{Content: string(out)}, nil
		},
	})

	r.register(ToolSpec{
		Name:        "wait",
		Description: "Wait up to 60 seconds for an async command_id to finish or emit new output.",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskRead,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command_id": map[string]any{"type": "string"},
				"seconds":    map[string]any{"type": "integer", "description": "Seconds to wait (1..60, default 10)."},
			},
			"required": []string{"command_id"},
		},
		Handler: func(ctx context.Context, _ CallContext, argsJSON string) (ToolOutput, error) {
			if mgr == nil {
				return ToolOutput{Content: "shell command manager unavailable", IsError: true}, nil
			}
			var args struct {
				CommandID string `json:"command_id"`
				Seconds   int    `json:"seconds"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			sec := args.Seconds
			if sec <= 0 {
				sec = 10
			}
			if sec > shellMaxWaitSec {
				sec = shellMaxWaitSec
			}
			job, err := mgr.WaitJob(ctx, args.CommandID, time.Duration(sec)*time.Second)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			chunk, nextOff, done, exitCode, errText, truncated := job.snapshot(-1)
			out, _ := json.Marshal(map[string]any{
				"command_id":  args.CommandID,
				"done":        done,
				"exit_code":   exitCode,
				"error":       errText,
				"output":      chunk,
				"next_offset": nextOff,
				"truncated":   truncated,
			})
			return ToolOutput{Content: string(out)}, nil
		},
	})
}
