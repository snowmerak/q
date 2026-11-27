//go:build windows

package main

import (
	"errors"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const approvedKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
const startupName = "Q Studio"

func autostartEnabled() (bool, error) {
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	return (startupRegistry{run: runKey, approved: approvedKey, name: startupName, executable: exe}).enabled()
}

type startupRegistry struct{ run, approved, name, executable string }

func (store startupRegistry) enabled() (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, store.run, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = key.Close() }()
	value, _, err := key.GetStringValue(store.name)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !strings.EqualFold(value, syscall.EscapeArg(store.executable)+" --autostart") {
		return false, nil
	}
	approved, err := registry.OpenKey(registry.CURRENT_USER, store.approved, registry.QUERY_VALUE)
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return false, err
	}
	if err == nil {
		defer func() { _ = approved.Close() }()
		data, _, e := approved.GetBinaryValue(store.name)
		if e == nil && len(data) > 0 && data[0]%2 == 1 {
			return false, nil
		}
	}
	return true, nil
}

func setAutostart(enabled bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return (startupRegistry{run: runKey, approved: approvedKey, name: startupName, executable: exe}).set(enabled)
}

func (store startupRegistry) set(enabled bool) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, store.run, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer func() { _ = key.Close() }()
	if !enabled {
		err = key.DeleteValue(store.name)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	if err = key.SetStringValue(store.name, syscall.EscapeArg(store.executable)+" --autostart"); err != nil {
		return err
	}
	// Enabling here is an explicit user action, so clear an OS-level disable.
	approved, err := registry.OpenKey(registry.CURRENT_USER, store.approved, registry.SET_VALUE)
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	if err == nil {
		defer func() { _ = approved.Close() }()
		err = approved.DeleteValue(store.name)
		if errors.Is(err, registry.ErrNotExist) {
			err = nil
		}
		return err
	}
	return nil
}

func trayAvailable() bool { return true }
