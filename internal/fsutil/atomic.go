package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to a temporary sibling file, syncs it, and
// atomically replaces path. The parent directory is created if absent.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tempPath := file.Name()
	replaced := false
	defer func() {
		_ = file.Close()
		if !replaced {
			_ = os.Remove(tempPath)
		}
	}()
	if err := file.Chmod(perm); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := replaceFile(tempPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	replaced = true
	return nil
}
