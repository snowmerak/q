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

	"github.com/snowmerak/q/thinker"
)

const (
	ThinkerCheckpointFileName       = "thinker-checkpoint.json"
	thinkerCheckpointCurrentVersion = 1
	maximumThinkerCheckpointSize    = 2 << 20
)

type thinkerCheckpointFile struct {
	Version    int                   `json:"version"`
	SessionID  string                `json:"session_id,omitempty"`
	UpdatedAt  time.Time             `json:"updated_at"`
	Checkpoint thinker.JobCheckpoint `json:"checkpoint"`
}

func (s Store) ThinkerCheckpointPath() string {
	return filepath.Join(s.SessionDir(), ThinkerCheckpointFileName)
}

// LoadThinkerCheckpoint implements thinker.JobCheckpointStore.
func (s Store) LoadThinkerCheckpoint() (thinker.JobCheckpoint, bool, error) {
	file, err := os.Open(s.ThinkerCheckpointPath())
	if errors.Is(err, os.ErrNotExist) {
		return thinker.JobCheckpoint{}, false, nil
	}
	if err != nil {
		return thinker.JobCheckpoint{}, false, fmt.Errorf("workspace: open %s: %w", s.ThinkerCheckpointPath(), err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return thinker.JobCheckpoint{}, false, fmt.Errorf("workspace: inspect %s: %w", s.ThinkerCheckpointPath(), err)
	}
	if info.Size() > maximumThinkerCheckpointSize {
		return thinker.JobCheckpoint{}, false, fmt.Errorf("workspace: Thinker checkpoint exceeds %d bytes", maximumThinkerCheckpointSize)
	}
	decoder := json.NewDecoder(io.LimitReader(file, maximumThinkerCheckpointSize))
	decoder.DisallowUnknownFields()
	var stored thinkerCheckpointFile
	if err := decoder.Decode(&stored); err != nil {
		return thinker.JobCheckpoint{}, false, fmt.Errorf("workspace: decode %s: %w", s.ThinkerCheckpointPath(), err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return thinker.JobCheckpoint{}, false, fmt.Errorf("workspace: decode %s: multiple JSON values", s.ThinkerCheckpointPath())
		}
		return thinker.JobCheckpoint{}, false, fmt.Errorf("workspace: decode %s: %w", s.ThinkerCheckpointPath(), err)
	}
	if stored.Version != thinkerCheckpointCurrentVersion {
		return thinker.JobCheckpoint{}, false, fmt.Errorf("workspace: unsupported Thinker checkpoint file version %d", stored.Version)
	}
	if s.SessionID != "" && stored.SessionID != "" && stored.SessionID != s.SessionID {
		return thinker.JobCheckpoint{}, false, fmt.Errorf(
			"workspace: Thinker checkpoint session ID %q does not match directory %q", stored.SessionID, s.SessionID,
		)
	}
	if err := thinker.ValidateJobCheckpoint(stored.Checkpoint); err != nil {
		return thinker.JobCheckpoint{}, false, fmt.Errorf("workspace: invalid Thinker checkpoint: %w", err)
	}
	return thinker.CloneJobCheckpoint(stored.Checkpoint), true, nil
}

// SaveThinkerCheckpoint implements thinker.JobCheckpointStore.
func (s Store) SaveThinkerCheckpoint(checkpoint thinker.JobCheckpoint) error {
	checkpoint = thinker.CloneJobCheckpoint(checkpoint)
	if err := thinker.ValidateJobCheckpoint(checkpoint); err != nil {
		return fmt.Errorf("workspace: invalid Thinker checkpoint: %w", err)
	}
	body, err := json.MarshalIndent(thinkerCheckpointFile{
		Version: thinkerCheckpointCurrentVersion, SessionID: s.SessionID,
		UpdatedAt: time.Now().UTC(), Checkpoint: checkpoint,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("workspace: encode Thinker checkpoint: %w", err)
	}
	if len(body) > maximumThinkerCheckpointSize {
		return fmt.Errorf("workspace: Thinker checkpoint exceeds %d bytes", maximumThinkerCheckpointSize)
	}
	body = append(body, '\n')
	return withLoomRootMutation(s.Root, func() error {
		if err := os.MkdirAll(s.SessionDir(), 0o700); err != nil {
			return fmt.Errorf("workspace: create %s: %w", s.SessionDir(), err)
		}
		if err := os.Chmod(s.SessionDir(), 0o700); err != nil {
			return fmt.Errorf("workspace: secure %s: %w", s.SessionDir(), err)
		}
		file, err := os.CreateTemp(s.SessionDir(), ".thinker-checkpoint-*.json")
		if err != nil {
			return fmt.Errorf("workspace: create temporary Thinker checkpoint: %w", err)
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
			return fmt.Errorf("workspace: secure temporary Thinker checkpoint: %w", err)
		}
		if _, err := file.Write(body); err != nil {
			_ = file.Close()
			return fmt.Errorf("workspace: write temporary Thinker checkpoint: %w", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return fmt.Errorf("workspace: sync temporary Thinker checkpoint: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("workspace: close temporary Thinker checkpoint: %w", err)
		}
		if err := replaceFile(temporaryPath, s.ThinkerCheckpointPath()); err != nil {
			return fmt.Errorf("workspace: replace %s: %w", s.ThinkerCheckpointPath(), err)
		}
		keep = true
		return nil
	})
}

// ClearThinkerCheckpoint removes only the checkpoint belonging to jobID. This
// guard prevents a late completion message from deleting a newer job's state.
func (s Store) ClearThinkerCheckpoint(jobID string) error {
	checkpoint, found, err := s.LoadThinkerCheckpoint()
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" || checkpoint.JobID != jobID {
		return fmt.Errorf("workspace: Thinker checkpoint belongs to job %q, not %q", checkpoint.JobID, jobID)
	}
	return s.ClearThinkerCheckpointAny()
}

// ClearThinkerCheckpointAny removes the fixed checkpoint during an explicit
// conversation reset or session deletion, including when its body is corrupt.
func (s Store) ClearThinkerCheckpointAny() error {
	return withLoomRootMutation(s.Root, func() error {
		err := os.Remove(s.ThinkerCheckpointPath())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("workspace: remove %s: %w", s.ThinkerCheckpointPath(), err)
		}
		return nil
	})
}
