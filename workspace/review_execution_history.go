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

type reviewExecutionFile struct {
	Version   int                         `json:"version"`
	SessionID string                      `json:"session_id,omitempty"`
	UpdatedAt time.Time                   `json:"updated_at"`
	Execution subagent.ReviewExecutionLog `json:"execution"`
}

// ReviewExecutionHistoryDir keeps standalone read-only reviews separate from
// plan and debug execution history.
func (s Store) ReviewExecutionHistoryDir() string {
	return filepath.Join(s.Dir(), "review-executions")
}

func (s Store) ArchiveReviewExecution(execution subagent.ReviewExecutionLog) (string, error) {
	return s.archiveReviewExecutionAt(execution, time.Now())
}

func (s Store) archiveReviewExecutionAt(execution subagent.ReviewExecutionLog, now time.Time) (string, error) {
	if err := validateReviewExecution(execution); err != nil {
		return "", fmt.Errorf("workspace: invalid review execution: %w", err)
	}
	body, err := json.MarshalIndent(reviewExecutionFile{
		Version: executionCurrentVersion, SessionID: s.SessionID,
		UpdatedAt: now.UTC(), Execution: execution,
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("workspace: encode review execution: %w", err)
	}
	if len(body) > maximumExecutionSize {
		return "", fmt.Errorf("workspace: review execution exceeds %d bytes", maximumExecutionSize)
	}
	body = append(body, '\n')

	var archivedPath string
	err = withLoomRootMutation(s.Root, func() error {
		directory := s.ReviewExecutionHistoryDir()
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("workspace: create review execution history: %w", err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("workspace: secure review execution history: %w", err)
		}
		file, err := os.CreateTemp(directory, ".review-execution-*.json")
		if err != nil {
			return fmt.Errorf("workspace: create temporary review execution: %w", err)
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
			return fmt.Errorf("workspace: secure temporary review execution: %w", err)
		}
		if _, err := file.Write(body); err != nil {
			_ = file.Close()
			return fmt.Errorf("workspace: write temporary review execution: %w", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return fmt.Errorf("workspace: sync temporary review execution: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("workspace: close temporary review execution: %w", err)
		}
		for range 4 {
			id, err := NewSessionID()
			if err != nil {
				return fmt.Errorf("workspace: create review execution ID: %w", err)
			}
			name := "review-execution-" + now.UTC().Format("20060102T150405.000000000Z") + "-" + id + ".json"
			target := filepath.Join(directory, name)
			if _, err := os.Lstat(target); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("workspace: inspect review execution history: %w", err)
			}
			if err := replaceFile(temporaryPath, target); err != nil {
				return fmt.Errorf("workspace: archive review execution: %w", err)
			}
			keep = true
			archivedPath = target
			return nil
		}
		return errors.New("workspace: could not allocate a unique review execution log")
	})
	return archivedPath, err
}

func validateReviewExecution(execution subagent.ReviewExecutionLog) error {
	if strings.TrimSpace(execution.Objective) == "" {
		return errors.New("objective is required")
	}
	if len(execution.Files) == 0 {
		return errors.New("at least one reviewed file is required")
	}
	switch execution.Outcome {
	case subagent.ReviewOutcomeSucceeded:
		if execution.Report == nil {
			return errors.New("succeeded review execution requires a report")
		}
	case subagent.ReviewOutcomeCanceled, subagent.ReviewOutcomeFailed:
		if strings.TrimSpace(execution.Error) == "" {
			return fmt.Errorf("%s review execution requires an error", execution.Outcome)
		}
	default:
		return fmt.Errorf("unsupported outcome %q", execution.Outcome)
	}
	if execution.StartedAt.IsZero() || execution.CompletedAt.IsZero() || execution.CompletedAt.Before(execution.StartedAt) {
		return errors.New("a valid time range is required")
	}
	return nil
}
