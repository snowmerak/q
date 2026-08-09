package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	IgnoreFileName    = ".qignore"
	maximumIgnoreSize = 1 << 20
)

func (s Store) IgnorePath() string {
	return filepath.Join(s.Root, IgnoreFileName)
}

func (s Store) LoadIgnore() (string, error) {
	body, err := os.ReadFile(s.IgnorePath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("workspace: read %s: %w", s.IgnorePath(), err)
	}
	if len(body) > maximumIgnoreSize {
		return "", fmt.Errorf("workspace: %s exceeds %d bytes", IgnoreFileName, maximumIgnoreSize)
	}
	return string(body), nil
}

func (s Store) SaveIgnore(content string) error {
	body := []byte(content)
	if len(body) > maximumIgnoreSize {
		return fmt.Errorf("workspace: %s exceeds %d bytes", IgnoreFileName, maximumIgnoreSize)
	}
	if len(body) > 0 && body[len(body)-1] != '\n' {
		body = append(body, '\n')
	}
	file, err := os.CreateTemp(s.Root, ".qignore-*")
	if err != nil {
		return fmt.Errorf("workspace: create temporary %s: %w", IgnoreFileName, err)
	}
	temporaryPath := file.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	permission := os.FileMode(0o644)
	if info, statErr := os.Stat(s.IgnorePath()); statErr == nil {
		permission = info.Mode().Perm()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		_ = file.Close()
		return fmt.Errorf("workspace: inspect %s: %w", s.IgnorePath(), statErr)
	}
	if err := file.Chmod(permission); err != nil {
		_ = file.Close()
		return fmt.Errorf("workspace: set %s permissions: %w", IgnoreFileName, err)
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return fmt.Errorf("workspace: write temporary %s: %w", IgnoreFileName, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("workspace: sync temporary %s: %w", IgnoreFileName, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("workspace: close temporary %s: %w", IgnoreFileName, err)
	}
	if err := replaceFile(temporaryPath, s.IgnorePath()); err != nil {
		return fmt.Errorf("workspace: replace %s: %w", s.IgnorePath(), err)
	}
	keep = true
	return nil
}
