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

// SessionModelConfig selects an API model or ACP connection for one session.
// Without an agent, an empty model inherits the workspace and global defaults,
// and empty reasoning effort inherits the global default. Selections survive
// conversation clear.
type SessionModelConfig struct {
	Model                string `json:"model"`
	Agent                string `json:"agent,omitempty"`
	AgentModel           string `json:"agent_model,omitempty"`
	AgentReasoningEffort string `json:"agent_reasoning_effort,omitempty"`
	ReasoningEffort      string `json:"reasoning_effort,omitempty"`
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
	if value.Agent != "" {
		if value.Agent != strings.TrimSpace(value.Agent) || strings.ContainsAny(value.Agent, "/\\ \t\r\n") || len(value.Agent) > 128 {
			return errors.New("workspace: invalid ACP connection ID")
		}
		if value.Model != "" || value.ReasoningEffort != "" {
			return errors.New("workspace: ACP sessions use agent-specific model and reasoning settings")
		}
	}
	if value.Model != strings.TrimSpace(value.Model) || len(value.Model) > 4096 {
		return errors.New("workspace: session model must be at most 4096 bytes without surrounding whitespace")
	}
	for _, choice := range []struct{ name, value string }{{"model", value.AgentModel}, {"reasoning effort", value.AgentReasoningEffort}} {
		if choice.value != "" && value.Agent == "" {
			return fmt.Errorf("workspace: an ACP %s requires an ACP connection", choice.name)
		}
		if choice.value != strings.TrimSpace(choice.value) || len(choice.value) > 4096 {
			return fmt.Errorf("workspace: ACP %s must be at most 4096 bytes without surrounding whitespace", choice.name)
		}
	}
	if strings.ContainsAny(value.ReasoningEffort, " \t\r\n") || len(value.ReasoningEffort) > 64 {
		return errors.New("workspace: reasoning effort must be at most 64 bytes without surrounding whitespace")
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
