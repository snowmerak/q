package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func chromeCandidates(goos, home string, getenv func(string) string) []string {
	paths := []string{"google-chrome", "google-chrome-stable", "chrome", "chromium", "chromium-browser"}
	switch goos {
	case "windows":
		for _, root := range []string{getenv("LOCALAPPDATA"), getenv("ProgramFiles"), getenv("ProgramFiles(x86)"), getenv("ProgramW6432")} {
			if root != "" && filepath.IsAbs(root) {
				paths = append(paths, filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"), filepath.Join(root, "Chromium", "Application", "chrome.exe"))
			}
		}
	case "darwin":
		roots := []string{"/Applications"}
		if home != "" {
			roots = append(roots, filepath.Join(home, "Applications"))
		}
		for _, root := range roots {
			paths = append(paths, filepath.Join(root, "Google Chrome.app", "Contents", "MacOS", "Google Chrome"), filepath.Join(root, "Chromium.app", "Contents", "MacOS", "Chromium"))
		}
	default:
		paths = append(paths, "/usr/bin/google-chrome", "/usr/bin/google-chrome-stable", "/usr/bin/chromium", "/usr/bin/chromium-browser", "/opt/google/chrome/chrome", "/snap/bin/chromium")
	}
	return paths
}

func discoverChrome(override string, candidates []string, lookup func(string) (string, error)) (string, error) {
	if override != "" {
		path, err := lookup(override)
		if err != nil {
			return "", invalid("QDRIVER_CHROME executable: %v", err)
		}
		return filepath.Abs(path)
	}
	for _, candidate := range candidates {
		if path, err := lookup(candidate); err == nil {
			return filepath.Abs(path)
		}
	}
	return "", &commandError{Code: "browser_not_found", Message: "Chrome/Chromium was not found on PATH or in standard installation locations; supply --chrome PATH, set QDRIVER_CHROME, or use --endpoint URL for an existing debugging browser"}
}

// Discovery only chooses an installed executable. Managed launch still creates
// the session's dedicated profile; it never attaches to a personal browser.
func autoChrome(serve serveOptions) (serveOptions, error) {
	if serve.Chrome != "" || serve.Endpoint != "" {
		return serve, nil
	}
	home, _ := os.UserHomeDir()
	path, err := discoverChrome(os.Getenv("QDRIVER_CHROME"), chromeCandidates(runtime.GOOS, home, os.Getenv), exec.LookPath)
	if err != nil {
		return serve, fmt.Errorf("prepare browser: %w", err)
	}
	serve.Chrome = path
	return serve, nil
}
