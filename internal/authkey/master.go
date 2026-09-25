package authkey

import (
	"errors"
	"fmt"
	"os"
)

// LoadMasterKey reads a raw 256-bit service master key.
func LoadMasterKey(path, name string) ([32]byte, error) {
	var result [32]byte
	body, err := os.ReadFile(path)
	if err != nil {
		return result, fmt.Errorf("%s: read master key: %w", name, err)
	}
	if len(body) != len(result) {
		return result, fmt.Errorf("%s: master key must be %d bytes", name, len(result))
	}
	copy(result[:], body)
	return result, nil
}

// EnsureMasterKey loads an existing key or exclusively creates it after
// prepare has secured its parent directory.
func EnsureMasterKey(path, name string, candidate [32]byte, prepare func() error) ([32]byte, error) {
	if existing, err := LoadMasterKey(path, name); err == nil {
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return [32]byte{}, err
	}
	if err := prepare(); err != nil {
		return [32]byte{}, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return LoadMasterKey(path, name)
	}
	if err != nil {
		return [32]byte{}, fmt.Errorf("%s: create master key: %w", name, err)
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return [32]byte{}, fmt.Errorf("%s: secure master key: %w", name, err)
	}
	if _, err := file.Write(candidate[:]); err != nil {
		return [32]byte{}, fmt.Errorf("%s: write master key: %w", name, err)
	}
	if err := file.Sync(); err != nil {
		return [32]byte{}, fmt.Errorf("%s: sync master key: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return [32]byte{}, fmt.Errorf("%s: close master key: %w", name, err)
	}
	keep = true
	return candidate, nil
}
