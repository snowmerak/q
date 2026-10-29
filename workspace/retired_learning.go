package workspace

import (
	"errors"
	"os"
	"path/filepath"
)

// Old extraction checkpoints are cleanup-only artifacts. They are never read
// or replayed after learning retirement, even when a session is restored.
const ThinkerCheckpointFileName = "thinker-checkpoint.json"

func (s Store) ThinkerCheckpointPath() string {
	return filepath.Join(s.SessionDir(), ThinkerCheckpointFileName)
}
func (s Store) ClearThinkerCheckpointAny() error {
	return withLoomRootMutation(s.Root, func() error {
		err := os.Remove(s.ThinkerCheckpointPath())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	})
}
