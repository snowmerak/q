package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/snowmerak/q/subagent"
)

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
