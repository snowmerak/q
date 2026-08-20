package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/config"
)

const (
	ModelFileName       = "model.json"
	ModelConfigVersion  = 1
	maximumModelCfgSize = 64 << 10
)

// ModelOverride selects one model within a workspace and caches its context
// window for main-chat memory policy when the target is the default role.
type ModelOverride struct {
	Model         string `json:"model"`
	ContextWindow int64  `json:"context_window,omitempty"`
}

// ModelConfig stores per-role model overrides for one workspace. Which role
// names may be overridden is enforced by the application because shared-state
// roles remain global.
type ModelConfig struct {
	Version   int                      `json:"version"`
	Overrides map[string]ModelOverride `json:"overrides,omitempty"`
}

func (s Store) ModelPath() string { return filepath.Join(s.Dir(), ModelFileName) }

// LoadModelConfig returns an empty versioned configuration when the workspace
// does not have a model override.
func (s Store) LoadModelConfig() (ModelConfig, error) {
	file, err := os.Open(s.ModelPath())
	if errors.Is(err, os.ErrNotExist) {
		return ModelConfig{Version: ModelConfigVersion}, nil
	}
	if err != nil {
		return ModelConfig{}, fmt.Errorf("workspace: open %s: %w", s.ModelPath(), err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ModelConfig{}, fmt.Errorf("workspace: inspect %s: %w", s.ModelPath(), err)
	}
	if info.Size() > maximumModelCfgSize {
		return ModelConfig{}, fmt.Errorf("workspace: model settings exceed %d bytes", maximumModelCfgSize)
	}
	decoder := json.NewDecoder(io.LimitReader(file, maximumModelCfgSize+1))
	decoder.DisallowUnknownFields()
	var value ModelConfig
	if err := decoder.Decode(&value); err != nil {
		return ModelConfig{}, fmt.Errorf("workspace: decode %s: %w", s.ModelPath(), err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return ModelConfig{}, fmt.Errorf("workspace: decode %s: multiple JSON values", s.ModelPath())
		}
		return ModelConfig{}, fmt.Errorf("workspace: decode %s: %w", s.ModelPath(), err)
	}
	if err := value.Validate(); err != nil {
		return ModelConfig{}, fmt.Errorf("workspace: decode %s: %w", s.ModelPath(), err)
	}
	return value, nil
}

func (c ModelConfig) Validate() error {
	if c.Version != ModelConfigVersion {
		return fmt.Errorf("unsupported model settings version %d", c.Version)
	}
	for role, override := range c.Overrides {
		if strings.TrimSpace(role) == "" || role != strings.TrimSpace(role) {
			return errors.New("workspace model role must be non-empty without surrounding whitespace")
		}
		if strings.TrimSpace(override.Model) == "" || override.Model != strings.TrimSpace(override.Model) {
			return fmt.Errorf("workspace model for role %q must be non-empty without surrounding whitespace", role)
		}
		if override.ContextWindow < 0 {
			return fmt.Errorf("workspace model context window for role %q must not be negative", role)
		}
		if !ModelOverrideAllowed(role) {
			return fmt.Errorf("workspace model role %q is shared globally or unsupported", role)
		}
	}
	return nil
}

// ModelOverrideAllowed reports whether a target may have workspace-local model
// state. Embedding, Thinker, and Librarian remain global shared services.
func ModelOverrideAllowed(role string) bool {
	if role == "default" {
		return true
	}
	if role == config.AgentRoleThinker || role == config.AgentRoleLibrarian {
		return false
	}
	return config.IsAgentRole(role)
}

func (s Store) SaveModelConfig(value ModelConfig) error {
	value.Version = ModelConfigVersion
	if err := value.Validate(); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("workspace: encode model settings: %w", err)
	}
	body = append(body, '\n')
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		return fmt.Errorf("workspace: create %s: %w", s.Dir(), err)
	}
	if err := os.Chmod(s.Dir(), 0o700); err != nil {
		return fmt.Errorf("workspace: secure %s: %w", s.Dir(), err)
	}
	file, err := os.CreateTemp(s.Dir(), ".model-*.json")
	if err != nil {
		return fmt.Errorf("workspace: create temporary model settings: %w", err)
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
		return fmt.Errorf("workspace: secure temporary model settings: %w", err)
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return fmt.Errorf("workspace: write temporary model settings: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("workspace: sync temporary model settings: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("workspace: close temporary model settings: %w", err)
	}
	if err := replaceFile(temporaryPath, s.ModelPath()); err != nil {
		return fmt.Errorf("workspace: replace %s: %w", s.ModelPath(), err)
	}
	keep = true
	return nil
}

func (s Store) ClearModelConfig() error {
	err := os.Remove(s.ModelPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("workspace: remove %s: %w", s.ModelPath(), err)
	}
	return nil
}
