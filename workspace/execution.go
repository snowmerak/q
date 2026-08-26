package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/snowmerak/q/subagent"
)

const (
	ExecutionFileName       = "plan-execution.json"
	executionCurrentVersion = 1
	maximumExecutionSize    = 8 << 20
)

var ErrExecutionNotFound = errors.New("q workspace plan execution not found")

type executionFile struct {
	Version    int                          `json:"version"`
	SessionID  string                       `json:"session_id,omitempty"`
	UpdatedAt  time.Time                    `json:"updated_at"`
	Checkpoint subagent.ExecutionCheckpoint `json:"checkpoint"`
}

func (s Store) ExecutionPath() string {
	return filepath.Join(s.SessionDir(), ExecutionFileName)
}

func (s Store) LoadExecution() (subagent.ExecutionCheckpoint, error) {
	file, err := os.Open(s.ExecutionPath())
	if errors.Is(err, os.ErrNotExist) {
		return subagent.ExecutionCheckpoint{}, ErrExecutionNotFound
	}
	if err != nil {
		return subagent.ExecutionCheckpoint{}, fmt.Errorf("workspace: open %s: %w", s.ExecutionPath(), err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return subagent.ExecutionCheckpoint{}, fmt.Errorf("workspace: inspect %s: %w", s.ExecutionPath(), err)
	}
	if info.Size() > maximumExecutionSize {
		return subagent.ExecutionCheckpoint{}, fmt.Errorf("workspace: plan execution exceeds %d bytes", maximumExecutionSize)
	}
	decoder := json.NewDecoder(io.LimitReader(file, maximumExecutionSize))
	decoder.DisallowUnknownFields()
	var stored executionFile
	if err := decoder.Decode(&stored); err != nil {
		return subagent.ExecutionCheckpoint{}, fmt.Errorf("workspace: decode %s: %w", s.ExecutionPath(), err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return subagent.ExecutionCheckpoint{}, fmt.Errorf("workspace: decode %s: multiple JSON values", s.ExecutionPath())
		}
		return subagent.ExecutionCheckpoint{}, fmt.Errorf("workspace: decode %s: %w", s.ExecutionPath(), err)
	}
	if stored.Version != executionCurrentVersion {
		return subagent.ExecutionCheckpoint{}, fmt.Errorf("workspace: unsupported plan execution version %d", stored.Version)
	}
	if s.SessionID != "" && stored.SessionID != "" && stored.SessionID != s.SessionID {
		return subagent.ExecutionCheckpoint{}, fmt.Errorf(
			"workspace: plan execution session ID %q does not match directory %q", stored.SessionID, s.SessionID,
		)
	}
	if err := validateStoredExecution(stored.Checkpoint); err != nil {
		return subagent.ExecutionCheckpoint{}, fmt.Errorf("workspace: invalid plan execution: %w", err)
	}
	return stored.Checkpoint, nil
}

func (s Store) SaveExecution(checkpoint subagent.ExecutionCheckpoint) error {
	if err := validateStoredExecution(checkpoint); err != nil {
		return fmt.Errorf("workspace: invalid plan execution: %w", err)
	}
	body, err := json.MarshalIndent(executionFile{
		Version: executionCurrentVersion, SessionID: s.SessionID,
		UpdatedAt: time.Now().UTC(), Checkpoint: checkpoint,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("workspace: encode plan execution: %w", err)
	}
	if len(body) > maximumExecutionSize {
		return fmt.Errorf("workspace: plan execution exceeds %d bytes", maximumExecutionSize)
	}
	body = append(body, '\n')
	return withLoomRootMutation(s.Root, func() error {
		if err := os.MkdirAll(s.SessionDir(), 0o700); err != nil {
			return fmt.Errorf("workspace: create %s: %w", s.SessionDir(), err)
		}
		if err := os.Chmod(s.SessionDir(), 0o700); err != nil {
			return fmt.Errorf("workspace: secure %s: %w", s.SessionDir(), err)
		}
		file, err := os.CreateTemp(s.SessionDir(), ".plan-execution-*.json")
		if err != nil {
			return fmt.Errorf("workspace: create temporary plan execution: %w", err)
		}
		temporaryPath := file.Name()
		keep := false
		defer func() {
			if !keep {
				_ = os.Remove(temporaryPath)
			}
		}()
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return fmt.Errorf("workspace: secure temporary plan execution: %w", err)
		}
		if _, err := file.Write(body); err != nil {
			_ = file.Close()
			return fmt.Errorf("workspace: write temporary plan execution: %w", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return fmt.Errorf("workspace: sync temporary plan execution: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("workspace: close temporary plan execution: %w", err)
		}
		if err := replaceFile(temporaryPath, s.ExecutionPath()); err != nil {
			return fmt.Errorf("workspace: replace %s: %w", s.ExecutionPath(), err)
		}
		keep = true
		return nil
	})
}

func (s Store) ClearExecution() error {
	return withLoomRootMutation(s.Root, func() error {
		err := os.Remove(s.ExecutionPath())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("workspace: remove %s: %w", s.ExecutionPath(), err)
		}
		return nil
	})
}

func validateStoredExecution(checkpoint subagent.ExecutionCheckpoint) error {
	if strings.TrimSpace(checkpoint.ExecutionID) == "" {
		return errors.New("execution ID is required")
	}
	if strings.TrimSpace(checkpoint.RunID) == "" {
		return errors.New("run ID is required")
	}
	return subagent.ValidateExecutionCheckpoint(checkpoint)
}
