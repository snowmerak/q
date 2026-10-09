package fsreplace

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile writes and syncs a temporary file beside path before replacing it.
// The parent directory must exist. Temporary files are cleaned up on failure;
// replacement uses the host's Replace semantics.
func WriteFile(path string, body []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporary := file.Name()
	replaced := false
	defer func() {
		_ = file.Close()
		if !replaced {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(mode); err != nil {
		return fmt.Errorf("secure temporary file: %w", err)
	}
	if _, err := file.Write(body); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := Replace(temporary, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	replaced = true
	return nil
}
