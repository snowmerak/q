package gatewayconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	FileName      = "gateway.json"
	MasterKeyName = "gateway.key"
)

var ErrNotFound = errors.New("q Gateway settings not found")

type Store struct {
	Dir string
}

func (s Store) Path() string { return filepath.Join(s.Dir, FileName) }

func (s Store) MasterKeyPath() string { return filepath.Join(s.Dir, MasterKeyName) }

func (s Store) Load() (Config, error) {
	file, err := os.Open(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, ErrNotFound
	}
	if err != nil {
		return Config{}, fmt.Errorf("gatewayconfig: open %s: %w", s.Path(), err)
	}
	defer file.Close()
	var value Config
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return Config{}, fmt.Errorf("gatewayconfig: decode %s: %w", s.Path(), err)
	}
	value = value.Effective()
	if err := value.Validate(); err != nil {
		return Config{}, err
	}
	return value, nil
}

func (s Store) LoadOrDefault() (Config, error) {
	value, err := s.Load()
	if errors.Is(err, ErrNotFound) {
		return Default(), nil
	}
	return value, err
}

func (s Store) Save(value Config) error {
	value = value.Effective()
	value.Version = CurrentVersion
	if err := value.Validate(); err != nil {
		return err
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("gatewayconfig: encode: %w", err)
	}
	body = append(body, '\n')
	return s.writeAtomic(s.Path(), ".gateway-*.json", body)
}

func (s Store) LoadMasterKey() ([32]byte, error) {
	var result [32]byte
	body, err := os.ReadFile(s.MasterKeyPath())
	if err != nil {
		return result, fmt.Errorf("gatewayconfig: read master key: %w", err)
	}
	if len(body) != len(result) {
		return result, fmt.Errorf("gatewayconfig: master key must be %d bytes", len(result))
	}
	copy(result[:], body)
	return result, nil
}

func (s Store) EnsureMasterKey(random [32]byte) ([32]byte, error) {
	if existing, err := s.LoadMasterKey(); err == nil {
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return [32]byte{}, err
	}
	if err := secureDirectory(s.Dir); err != nil {
		return [32]byte{}, err
	}
	file, err := os.OpenFile(s.MasterKeyPath(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return s.LoadMasterKey()
	}
	if err != nil {
		return [32]byte{}, fmt.Errorf("gatewayconfig: create master key: %w", err)
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(s.MasterKeyPath())
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return [32]byte{}, fmt.Errorf("gatewayconfig: secure master key: %w", err)
	}
	if _, err := file.Write(random[:]); err != nil {
		return [32]byte{}, fmt.Errorf("gatewayconfig: write master key: %w", err)
	}
	if err := file.Sync(); err != nil {
		return [32]byte{}, fmt.Errorf("gatewayconfig: sync master key: %w", err)
	}
	if err := file.Close(); err != nil {
		return [32]byte{}, fmt.Errorf("gatewayconfig: close master key: %w", err)
	}
	keep = true
	return random, nil
}

func (s Store) writeAtomic(path, pattern string, body []byte) error {
	if err := secureDirectory(s.Dir); err != nil {
		return err
	}
	file, err := os.CreateTemp(s.Dir, pattern)
	if err != nil {
		return fmt.Errorf("gatewayconfig: create temporary file: %w", err)
	}
	temporaryPath := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("gatewayconfig: secure temporary file: %w", err)
	}
	if _, err := file.Write(body); err != nil {
		return fmt.Errorf("gatewayconfig: write temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("gatewayconfig: sync temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("gatewayconfig: close temporary file: %w", err)
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return fmt.Errorf("gatewayconfig: replace %s: %w", path, err)
	}
	keep = true
	return nil
}

func secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("gatewayconfig: create %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("gatewayconfig: secure %s: %w", path, err)
	}
	return nil
}
