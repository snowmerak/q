package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type desktopSettings struct {
	KeepRunning bool `json:"keep_running"`
	StartHidden bool `json:"start_hidden"`
}

func loadSettings(directory string) (desktopSettings, error) {
	settings := desktopSettings{KeepRunning: true, StartHidden: true}
	body, err := os.ReadFile(filepath.Join(directory, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	return settings, json.Unmarshal(body, &settings)
}

func saveSettings(directory string, settings desktopSettings) error {
	body, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".settings-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(body)
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(directory, "settings.json"))
}
