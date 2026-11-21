package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/internal/fsreplace"
)

const subagentTargetsFile = "subagent-targets.json"

// LoadSubagentTargets reads execution choices keyed by canonical profile ID.
func (s Store) LoadSubagentTargets() (map[string]config.SubagentTarget, error) {
	path := filepath.Join(s.Dir(), subagentTargetsFile)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]config.SubagentTarget), nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	contents, err := io.ReadAll(io.LimitReader(file, maximumModelCfgSize+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maximumModelCfgSize {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maximumModelCfgSize)
	}
	targets := make(map[string]config.SubagentTarget)
	if err := json.Unmarshal(contents, &targets); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if targets == nil {
		targets = make(map[string]config.SubagentTarget)
	}
	for id, target := range targets {
		if err := target.Validate(); err != nil {
			return nil, fmt.Errorf("subagent %q: %w", id, err)
		}
	}
	return targets, nil
}

func (s Store) SaveSubagentTargets(targets map[string]config.SubagentTarget) error {
	contents, err := json.MarshalIndent(targets, "", "  ")
	if err != nil {
		return err
	}
	if len(contents)+1 > maximumModelCfgSize {
		return fmt.Errorf("subagent targets exceed %d bytes", maximumModelCfgSize)
	}
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		return err
	}
	return fsreplace.WriteFile(filepath.Join(s.Dir(), subagentTargetsFile), append(contents, '\n'), 0o600)
}
