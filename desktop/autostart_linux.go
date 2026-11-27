//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

func loginDesktopPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "autostart", "q-studio.desktop"), nil
}
func autostartEnabled() (bool, error) {
	path, err := loginDesktopPath()
	if err != nil {
		return false, err
	}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	text := string(body)
	return !strings.Contains(text, "Hidden=true") && !strings.Contains(text, "X-GNOME-Autostart-enabled=false"), nil
}
func setAutostart(enabled bool) error {
	path, err := loginDesktopPath()
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
	escaped := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "`", "\\`", "$", "\\$", "%", "%%").Replace(exe)
	body := "[Desktop Entry]\nType=Application\nName=Q Studio\nExec=\"" + escaped + "\" --autostart\nTerminal=false\nX-GNOME-Autostart-enabled=true\n"
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o600)
}
func trayAvailable() bool {
	result := make(chan bool, 1)
	go func() {
		bus, err := dbus.SessionBus()
		if err != nil {
			result <- false
			return
		}
		var owned bool
		err = bus.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&owned)
		result <- err == nil && owned
	}()
	select {
	case available := <-result:
		return available
	case <-time.After(time.Second):
		return false
	}
}
