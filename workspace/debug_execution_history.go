package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/snowmerak/q/subagent"
)

type debugExecutionFile struct {
	Version   int                        `json:"version"`
	SessionID string                     `json:"session_id,omitempty"`
	UpdatedAt time.Time                  `json:"updated_at"`
	Execution subagent.DebugExecutionLog `json:"execution"`
}

// DebugExecutionHistoryDir keeps completed /debug investigations separate
// from plan execution history and session-scoped state.
func (s Store) DebugExecutionHistoryDir() string {
	return filepath.Join(s.Dir(), "debug-executions")
}

// ArchiveDebugExecution writes one completed, canceled, or failed /debug
// investigation as a timestamped audit log. Debug investigations are reports,
// not resumable checkpoints, so they are written directly to history.
func (s Store) ArchiveDebugExecution(execution subagent.DebugExecutionLog) (string, error) {
	return s.archiveDebugExecutionAt(execution, time.Now())
}

func (s Store) archiveDebugExecutionAt(execution subagent.DebugExecutionLog, now time.Time) (string, error) {
	if err := validateDebugExecution(execution); err != nil {
		return "", fmt.Errorf("workspace: invalid debug execution: %w", err)
	}
	body, err := json.MarshalIndent(debugExecutionFile{
		Version: executionCurrentVersion, SessionID: s.SessionID,
		UpdatedAt: now.UTC(), Execution: execution,
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("workspace: encode debug execution: %w", err)
	}
	if len(body) > maximumExecutionSize {
		return "", fmt.Errorf("workspace: debug execution exceeds %d bytes", maximumExecutionSize)
	}
	body = append(body, '\n')

	var archivedPath string
	err = withLoomRootMutation(s.Root, func() error {
		directory := s.DebugExecutionHistoryDir()
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("workspace: create debug execution history: %w", err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("workspace: secure debug execution history: %w", err)
		}
		file, err := os.CreateTemp(directory, ".debug-execution-*.json")
		if err != nil {
			return fmt.Errorf("workspace: create temporary debug execution: %w", err)
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
			return fmt.Errorf("workspace: secure temporary debug execution: %w", err)
		}
		if _, err := file.Write(body); err != nil {
			_ = file.Close()
			return fmt.Errorf("workspace: write temporary debug execution: %w", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return fmt.Errorf("workspace: sync temporary debug execution: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("workspace: close temporary debug execution: %w", err)
		}
		for range 4 {
			id, err := NewSessionID()
			if err != nil {
				return fmt.Errorf("workspace: create debug execution ID: %w", err)
			}
			name := "debug-execution-" + now.UTC().Format("20060102T150405.000000000Z") + "-" + id + ".json"
			target := filepath.Join(directory, name)
			if _, err := os.Lstat(target); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("workspace: inspect debug execution history: %w", err)
			}
			if err := replaceFile(temporaryPath, target); err != nil {
				return fmt.Errorf("workspace: archive debug execution: %w", err)
			}
			keep = true
			archivedPath = target
			return nil
		}
		return errors.New("workspace: could not allocate a unique debug execution log")
	})
	return archivedPath, err
}

func validateDebugExecution(execution subagent.DebugExecutionLog) error {
	if strings.TrimSpace(execution.Objective) == "" {
		return errors.New("objective is required")
	}
	switch execution.Outcome {
	case subagent.DebugOutcomeSucceeded:
		if execution.Report == nil {
			return errors.New("succeeded debug execution requires a report")
		}
	case subagent.DebugOutcomeCanceled, subagent.DebugOutcomeFailed:
		if strings.TrimSpace(execution.Error) == "" {
			return fmt.Errorf("%s debug execution requires an error", execution.Outcome)
		}
	default:
		return fmt.Errorf("unsupported outcome %q", execution.Outcome)
	}
	if execution.StartedAt.IsZero() || execution.CompletedAt.IsZero() || execution.CompletedAt.Before(execution.StartedAt) {
		return errors.New("a valid time range is required")
	}
	return nil
}
