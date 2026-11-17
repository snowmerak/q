//go:build darwin

package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
)

func loginAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", "ne.saturday.q-studio.plist"), err
}
func autostartEnabled() (bool, error) {
	path, err := loginAgentPath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
func setAutostart(enabled bool) error {
	path, err := loginAgentPath()
	if err != nil {
		return err
	}
	if !enabled {
		err = os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	var escaped bytes.Buffer
	if err = xml.EscapeText(&escaped, []byte(exe)); err != nil {
		return err
	}
	// A LaunchAgent preserves --autostart on packaged apps too; SMAppService's
	// main-app registration does not preserve custom launch arguments.
	body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Label</key><string>ne.saturday.q-studio</string><key>ProgramArguments</key><array><string>` + escaped.String() + `</string><string>--autostart</string></array><key>RunAtLoad</key><true/></dict></plist>`
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o600)
}
func trayAvailable() bool { return true }
