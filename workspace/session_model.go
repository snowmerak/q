package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/internal/fsreplace"
)

// SessionModelConfig selects the interactive model for one session. An empty
// model inherits the workspace and global defaults. It survives conversation clear.
type SessionModelConfig struct {
	Model string `json:"model"`
}

func (s Store) SessionModelPath() string { return filepath.Join(s.SessionDir(), ModelFileName) }

func (s Store) LoadSessionModelConfig() (SessionModelConfig, error) {
	if s.SessionID == "" {
		return SessionModelConfig{}, nil
	}
	file, err := os.Open(s.SessionModelPath())
	if errors.Is(err, os.ErrNotExist) {
		return SessionModelConfig{}, nil
	}
	if err != nil {
		return SessionModelConfig{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return SessionModelConfig{}, err
	}
	if info.Size() > maximumModelCfgSize {
		return SessionModelConfig{}, errors.New("workspace: session model settings exceed size limit")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maximumModelCfgSize+1))
	decoder.DisallowUnknownFields()
	var value SessionModelConfig
	if err := decoder.Decode(&value); err != nil {
		return SessionModelConfig{}, fmt.Errorf("workspace: decode session model: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return SessionModelConfig{}, errors.New("workspace: session model contains trailing data")
	}
	if err := value.Validate(); err != nil {
		return SessionModelConfig{}, err
	}
	return value, nil
}

func (value SessionModelConfig) Validate() error {
	if value.Model != strings.TrimSpace(value.Model) || len(value.Model) > 4096 {
		return errors.New("workspace: session model must be at most 4096 bytes without surrounding whitespace")
	}
	return nil
}

// SaveSessionModelConfig requires the caller to own the session lock.
func (s Store) SaveSessionModelConfig(value SessionModelConfig) error {
	if err := validateSessionID(s.SessionID); err != nil {
		return err
	}
	if err := value.Validate(); err != nil {
		return err
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return withLoomRootMutation(s.Root, func() error {
		return fsreplace.WriteFile(s.SessionModelPath(), append(body, '\n'), 0o600)
	})
}
