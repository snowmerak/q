package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	LearningFileName       = "learning.json"
	LearningConfigVersion  = 1
	maximumLearningCfgSize = 4 << 10
)

// LearningConfig controls Thinker learning for one workspace. Learning is
// enabled by default; a persisted disabled setting pauses collection and queue
// processing without deleting already queued segments.
type LearningConfig struct {
	Version  int  `json:"version"`
	Disabled bool `json:"disabled"`
}

func (s Store) LearningPath() string { return filepath.Join(s.Dir(), LearningFileName) }

func (s Store) LoadLearningConfig() (LearningConfig, error) {
	file, err := os.Open(s.LearningPath())
	if errors.Is(err, os.ErrNotExist) {
		return LearningConfig{Version: LearningConfigVersion}, nil
	}
	if err != nil {
		return LearningConfig{}, fmt.Errorf("workspace: open %s: %w", s.LearningPath(), err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return LearningConfig{}, fmt.Errorf("workspace: inspect %s: %w", s.LearningPath(), err)
	}
	if info.Size() > maximumLearningCfgSize {
		return LearningConfig{}, fmt.Errorf("workspace: learning settings exceed %d bytes", maximumLearningCfgSize)
	}

	decoder := json.NewDecoder(io.LimitReader(file, maximumLearningCfgSize+1))
	decoder.DisallowUnknownFields()
	var value LearningConfig
	if err := decoder.Decode(&value); err != nil {
		return LearningConfig{}, fmt.Errorf("workspace: decode %s: %w", s.LearningPath(), err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return LearningConfig{}, fmt.Errorf("workspace: decode %s: multiple JSON values", s.LearningPath())
		}
		return LearningConfig{}, fmt.Errorf("workspace: decode %s: %w", s.LearningPath(), err)
	}
	if err := value.Validate(); err != nil {
		return LearningConfig{}, fmt.Errorf("workspace: decode %s: %w", s.LearningPath(), err)
	}
	return value, nil
}

func (c LearningConfig) Validate() error {
	if c.Version != LearningConfigVersion {
		return fmt.Errorf("unsupported learning settings version %d", c.Version)
	}
	return nil
}

func (s Store) SaveLearningConfig(value LearningConfig) error {
	value.Version = LearningConfigVersion
	if err := value.Validate(); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("workspace: encode learning settings: %w", err)
	}
	body = append(body, '\n')
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		return fmt.Errorf("workspace: create %s: %w", s.Dir(), err)
	}
	if err := os.Chmod(s.Dir(), 0o700); err != nil {
		return fmt.Errorf("workspace: secure %s: %w", s.Dir(), err)
	}
	file, err := os.CreateTemp(s.Dir(), ".learning-*.json")
	if err != nil {
		return fmt.Errorf("workspace: create temporary learning settings: %w", err)
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
		return fmt.Errorf("workspace: secure temporary learning settings: %w", err)
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return fmt.Errorf("workspace: write temporary learning settings: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("workspace: sync temporary learning settings: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("workspace: close temporary learning settings: %w", err)
	}
	if err := replaceFile(temporaryPath, s.LearningPath()); err != nil {
		return fmt.Errorf("workspace: replace %s: %w", s.LearningPath(), err)
	}
	keep = true
	return nil
}

func (s Store) ClearLearningConfig() error {
	err := os.Remove(s.LearningPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("workspace: remove %s: %w", s.LearningPath(), err)
	}
	return nil
}
