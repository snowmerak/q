// Package providerhost runs llm-provider's Gateway as a supervised child of q.
package providerhost

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/snowmerak/llm-provider/gateway"
)

const FileName = "providers.json"

var ErrNotFound = errors.New("q provider config not found")

type Store struct {
	Dir string
}

func (s Store) Path() string {
	return filepath.Join(s.Dir, FileName)
}

func (s Store) RuntimeDir() string {
	return filepath.Join(s.Dir, "runtime")
}

func (s Store) Load() (gateway.Config, error) {
	file, err := os.Open(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return gateway.Config{}, ErrNotFound
	}
	if err != nil {
		return gateway.Config{}, fmt.Errorf("providerhost: open %s: %w", s.Path(), err)
	}
	defer file.Close()

	var value gateway.Config
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return gateway.Config{}, fmt.Errorf("providerhost: decode %s: %w", s.Path(), err)
	}
	value.Listen = "127.0.0.1:0"
	return value, nil
}

func (s Store) Save(value gateway.Config) error {
	value.Listen = "127.0.0.1:0"
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("providerhost: encode config: %w", err)
	}
	body = append(body, '\n')
	return s.writeAtomic(s.Path(), body)
}

func (s Store) WriteRuntimeSnapshot(value gateway.Config) (string, error) {
	value.Listen = "127.0.0.1:0"
	body, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("providerhost: encode runtime config: %w", err)
	}
	if err := secureDirectory(s.RuntimeDir()); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(s.RuntimeDir(), "gateway-*.json")
	if err != nil {
		return "", fmt.Errorf("providerhost: create runtime config: %w", err)
	}
	path := file.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("providerhost: secure runtime config: %w", err)
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("providerhost: write runtime config: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("providerhost: close runtime config: %w", err)
	}
	keep = true
	return path, nil
}

func (s Store) writeAtomic(path string, body []byte) error {
	if err := secureDirectory(s.Dir); err != nil {
		return err
	}
	file, err := os.CreateTemp(s.Dir, ".providers-*.json")
	if err != nil {
		return fmt.Errorf("providerhost: create temporary config: %w", err)
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
		return fmt.Errorf("providerhost: secure temporary config: %w", err)
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return fmt.Errorf("providerhost: write temporary config: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("providerhost: sync temporary config: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("providerhost: close temporary config: %w", err)
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return fmt.Errorf("providerhost: replace %s: %w", path, err)
	}
	keep = true
	return nil
}

func secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("providerhost: create %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("providerhost: secure %s: %w", path, err)
	}
	return nil
}
