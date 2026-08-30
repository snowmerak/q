package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/snowmerak/q/subagent"
)

type planningFile struct {
	Version   int                  `json:"version"`
	SessionID string               `json:"session_id,omitempty"`
	UpdatedAt time.Time            `json:"updated_at"`
	Planning  subagent.PlanningLog `json:"planning"`
}

// ExecutionHistoryDir keeps completed plan snapshots separate from resumable
// checkpoints. Clearing or deleting a session does not delete these logs.
func (s Store) ExecutionHistoryDir() string {
	return filepath.Join(s.Dir(), "plan-executions")
}

// ArchiveExecution moves a completed checkpoint to a timestamped JSON log.
// The saved JSON (including session/run identity and final task results) is
// unchanged. On failure the active checkpoint is kept for a later retry.
// A missing checkpoint is a no-op, making finalization safe to retry.
func (s Store) ArchiveExecution() (string, error) {
	return s.archiveExecutionAt(time.Now())
}

// ArchivePlanning writes a human-readable audit log for planning workflows
// that ended before an execution checkpoint existed, such as cancellation or
// failure. Approved planning is instead embedded in its execution checkpoint.
func (s Store) ArchivePlanning(planning subagent.PlanningLog) (string, error) {
	return s.archivePlanningAt(planning, time.Now())
}

func (s Store) archivePlanningAt(planning subagent.PlanningLog, now time.Time) (string, error) {
	if planning.Outcome != subagent.PlanningOutcomeCanceled && planning.Outcome != subagent.PlanningOutcomeFailed {
		return "", fmt.Errorf("workspace: planning history requires canceled or failed outcome, got %q", planning.Outcome)
	}
	if planning.StartedAt.IsZero() || planning.CompletedAt.IsZero() || planning.CompletedAt.Before(planning.StartedAt) {
		return "", errors.New("workspace: planning history requires a valid time range")
	}
	body, err := json.MarshalIndent(planningFile{
		Version: executionCurrentVersion, SessionID: s.SessionID,
		UpdatedAt: now.UTC(), Planning: planning,
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("workspace: encode planning history: %w", err)
	}
	if len(body) > maximumExecutionSize {
		return "", fmt.Errorf("workspace: planning history exceeds %d bytes", maximumExecutionSize)
	}
	body = append(body, '\n')

	var archivedPath string
	err = withLoomRootMutation(s.Root, func() error {
		directory := s.ExecutionHistoryDir()
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("workspace: create plan execution history: %w", err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("workspace: secure plan execution history: %w", err)
		}
		file, err := os.CreateTemp(directory, ".plan-planning-*.json")
		if err != nil {
			return fmt.Errorf("workspace: create temporary planning history: %w", err)
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
			return fmt.Errorf("workspace: secure temporary planning history: %w", err)
		}
		if _, err := file.Write(body); err != nil {
			_ = file.Close()
			return fmt.Errorf("workspace: write temporary planning history: %w", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return fmt.Errorf("workspace: sync temporary planning history: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("workspace: close temporary planning history: %w", err)
		}
		for range 4 {
			id, err := NewSessionID()
			if err != nil {
				return fmt.Errorf("workspace: create planning history ID: %w", err)
			}
			name := "plan-planning-" + now.UTC().Format("20060102T150405.000000000Z") + "-" + id + ".json"
			target := filepath.Join(directory, name)
			if _, err := os.Lstat(target); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("workspace: inspect planning history: %w", err)
			}
			if err := replaceFile(temporaryPath, target); err != nil {
				return fmt.Errorf("workspace: archive planning history: %w", err)
			}
			keep = true
			archivedPath = target
			return nil
		}
		return errors.New("workspace: could not allocate a unique planning history log")
	})
	return archivedPath, err
}

func (s Store) archiveExecutionAt(now time.Time) (string, error) {
	var archivedPath string
	err := withLoomRootMutation(s.Root, func() error {
		checkpoint, err := s.LoadExecution()
		if errors.Is(err, ErrExecutionNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if checkpoint.Phase != subagent.ExecutionPhaseCompleted {
			return errors.New("workspace: only completed plan executions can be archived")
		}
		directory := s.ExecutionHistoryDir()
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("workspace: create plan execution history: %w", err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("workspace: secure plan execution history: %w", err)
		}
		for range 4 {
			id, err := NewSessionID()
			if err != nil {
				return fmt.Errorf("workspace: create plan execution log ID: %w", err)
			}
			name := "plan-execution-" + now.UTC().Format("20060102T150405.000000000Z") + "-" + id + ".json"
			target := filepath.Join(directory, name)
			// The workspace operation lock serializes all q log writers. Never
			// replace an existing log, even if an ID happens to collide.
			if _, err := os.Lstat(target); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("workspace: inspect plan execution log: %w", err)
			}
			if err := replaceFile(s.ExecutionPath(), target); err != nil {
				return fmt.Errorf("workspace: archive plan execution: %w", err)
			}
			archivedPath = target
			return nil
		}
		return errors.New("workspace: could not allocate a unique plan execution log")
	})
	return archivedPath, err
}
