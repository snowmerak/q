package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/snowmerak/q/internal/fsreplace"
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
	permission := os.FileMode(0o644)
	if info, statErr := os.Stat(s.IgnorePath()); statErr == nil {
		permission = info.Mode().Perm()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("workspace: inspect %s: %w", s.IgnorePath(), statErr)
	}
	if err := fsreplace.WriteFile(s.IgnorePath(), body, permission); err != nil {
		return fmt.Errorf("workspace: save %s: %w", IgnoreFileName, err)
	}
	return nil
}
