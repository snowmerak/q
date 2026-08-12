package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/snowmerak/q/lsp"
)

const (
	LSPFileName    = "lsp.json"
	maximumLSPSize = 4 << 20
)

func (s Store) LSPPath() string { return filepath.Join(s.Dir(), LSPFileName) }

func (s Store) LoadLSP() (lsp.WorkspaceConfig, error) {
	file, err := os.Open(s.LSPPath())
	if errors.Is(err, os.ErrNotExist) {
		return lsp.WorkspaceConfig{Version: lsp.WorkspaceConfigVersion}, nil
	}
	if err != nil {
		return lsp.WorkspaceConfig{}, fmt.Errorf("workspace: open %s: %w", s.LSPPath(), err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return lsp.WorkspaceConfig{}, fmt.Errorf("workspace: inspect %s: %w", s.LSPPath(), err)
	}
	if info.Size() > maximumLSPSize {
		return lsp.WorkspaceConfig{}, fmt.Errorf("workspace: LSP settings exceed %d bytes", maximumLSPSize)
	}
	decoder := json.NewDecoder(io.LimitReader(file, maximumLSPSize+1))
	decoder.DisallowUnknownFields()
	var value lsp.WorkspaceConfig
	if err := decoder.Decode(&value); err != nil {
		return lsp.WorkspaceConfig{}, fmt.Errorf("workspace: decode %s: %w", s.LSPPath(), err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return lsp.WorkspaceConfig{}, fmt.Errorf("workspace: decode %s: multiple JSON values", s.LSPPath())
		}
		return lsp.WorkspaceConfig{}, fmt.Errorf("workspace: decode %s: %w", s.LSPPath(), err)
	}
	for index := range value.Roots {
		normalized, err := lsp.NormalizeRoot(value.Roots[index])
		if err != nil {
			return lsp.WorkspaceConfig{}, fmt.Errorf("workspace: decode %s root %d: %w", s.LSPPath(), index+1, err)
		}
		value.Roots[index] = normalized
	}
	return value, nil
}

func (s Store) SaveLSP(value lsp.WorkspaceConfig, global lsp.GlobalConfig) error {
	value.Version = lsp.WorkspaceConfigVersion
	for index := range value.Roots {
		normalized, err := lsp.NormalizeRoot(value.Roots[index])
		if err != nil {
			return fmt.Errorf("workspace: LSP root %d: %w", index+1, err)
		}
		value.Roots[index] = normalized
	}
	if err := value.Validate(global); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("workspace: encode LSP settings: %w", err)
	}
	if len(body) > maximumLSPSize {
		return fmt.Errorf("workspace: LSP settings exceed %d bytes", maximumLSPSize)
	}
	body = append(body, '\n')
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		return fmt.Errorf("workspace: create %s: %w", s.Dir(), err)
	}
	file, err := os.CreateTemp(s.Dir(), ".lsp-*.json")
	if err != nil {
		return fmt.Errorf("workspace: create temporary LSP settings: %w", err)
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
		return err
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := replaceFile(temporaryPath, s.LSPPath()); err != nil {
		return fmt.Errorf("workspace: replace %s: %w", s.LSPPath(), err)
	}
	keep = true
	return nil
}
