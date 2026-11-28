package cli

import (
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestChromeDiscoveryOrderAndMissingBrowser(t *testing.T) {
	want := filepath.Join(t.TempDir(), "Chrome", "chrome.exe")
	var attempts []string
	lookup := func(name string) (string, error) {
		attempts = append(attempts, name)
		if name == "installed" || name == "selected" {
			return want, nil
		}
		return "", exec.ErrNotFound
	}
	path, err := discoverChrome("", []string{"missing", "installed", "later"}, lookup)
	if err != nil || path != want || !reflect.DeepEqual(attempts, []string{"missing", "installed"}) {
		t.Fatalf("ordered discovery: %s %v attempts=%v", path, err, attempts)
	}
	attempts = nil
	path, err = discoverChrome("selected", []string{"installed"}, lookup)
	if err != nil || path != want || !reflect.DeepEqual(attempts, []string{"selected"}) {
		t.Fatalf("environment override not preferred: %s %v attempts=%v", path, err, attempts)
	}
	attempts = nil
	_, err = discoverChrome("missing-override", []string{"installed"}, lookup)
	if err == nil || failure(err, false).Code != "invalid_argument" || len(attempts) != 1 {
		t.Fatalf("invalid override silently selected a different browser: %v %v", err, attempts)
	}
	_, err = discoverChrome("", []string{"missing"}, lookup)
	if err == nil || failure(err, false).Code != "browser_not_found" {
		t.Fatalf("missing browser has no actionable outcome: %v", err)
	}
}

func TestChromeStandardLocationsAndExplicitConfiguration(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	env := func(name string) string {
		if name == "LOCALAPPDATA" {
			return "relative-location"
		}
		if name == "ProgramFiles" {
			return root
		}
		return ""
	}
	for _, tc := range []struct{ os, expected string }{
		{"windows", filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe")},
		{"darwin", filepath.Join(home, "Applications", "Google Chrome.app", "Contents", "MacOS", "Google Chrome")},
		{"linux", "/opt/google/chrome/chrome"},
	} {
		candidates := chromeCandidates(tc.os, home, env)
		if !slices.Contains(candidates, tc.expected) || candidates[0] != "google-chrome" {
			t.Errorf("%s standard/PATH locations missing: %v", tc.os, candidates)
		}
		for _, candidate := range candidates {
			if filepath.Dir(candidate) == "relative-location" || candidate == "" {
				t.Errorf("relative/empty installation root searched: %q", candidate)
			}
		}
	}
	t.Setenv("QDRIVER_CHROME", filepath.Join(root, "missing-chrome"))
	for _, chosen := range []serveOptions{{Chrome: "chosen-executable"}, {Endpoint: "http://127.0.0.1:9222"}} {
		got, err := autoChrome(chosen)
		if err != nil || got != chosen {
			t.Fatalf("automatic discovery replaced explicit configuration: %+v %v", got, err)
		}
	}
	if _, err := autoChrome(serveOptions{}); err == nil || !errors.As(err, new(*commandError)) {
		t.Fatalf("environment override failure lost structured error: %v", err)
	}
}
