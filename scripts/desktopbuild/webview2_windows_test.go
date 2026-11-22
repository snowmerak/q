//go:build windows

package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// Exercise the actual NSIS routine in a silent harness. Bootstrapper fixtures
// only touch private test registry keys and temporary files, never WebView2.
func TestWebViewInstallerAdmission(t *testing.T) {
	nsis := os.Getenv("Q_STUDIO_NSIS")
	if nsis == "" {
		var err error
		nsis, err = exec.LookPath("makensis")
		if err != nil {
			t.Skip("set Q_STUDIO_NSIS to run native installer regression tests")
		}
	}
	include, err := filepath.Abs("../../desktop/packaging/webview2.nsh")
	if err != nil {
		t.Fatal(err)
	}
	const actualKey = `Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	tests := []struct {
		name, existing, newVersion string
		exit                       int
		success, bootstrap, actual bool
	}{
		{name: "installed_machine_runtime_skips_setup", success: true, actual: true},
		{name: "installed_user_runtime_skips_setup", existing: "157.0.4341.60", success: true},
		{name: "already_installed_result_without_runtime_fails", exit: -2147219416, bootstrap: true},
		{name: "zero_result_without_runtime_fails", bootstrap: true},
		{name: "nonzero_result_with_available_runtime_succeeds", newVersion: "157.0.4341.60", exit: -2147219416, success: true, bootstrap: true},
		{name: "new_runtime_succeeds", newVersion: "157.0.4341.60", success: true, bootstrap: true},
		{name: "zero_version_requires_setup", existing: "0.0.0.0", newVersion: "157.0.4341.60", success: true, bootstrap: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			keyPath := `Software\QStudioWebViewTest-` + rand.Text()
			if test.actual {
				keyPath = actualKey
				if !runtimeRegistered() {
					t.Skip("no existing WebView2 Runtime on this test machine")
				}
			} else {
				t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, keyPath) })
				if test.existing != "" {
					key, _, err := registry.CreateKey(registry.CURRENT_USER, keyPath, registry.SET_VALUE|registry.WOW64_32KEY)
					if err != nil {
						t.Fatal(err)
					}
					err = key.SetStringValue("pv", test.existing)
					_ = key.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			marker := filepath.Join(dir, "bootstrap-ran")
			passed := filepath.Join(dir, "admitted")
			bootstrap := filepath.Join(dir, "bootstrap.exe")
			register := ""
			if test.newVersion != "" {
				register = fmt.Sprintf("SetRegView 32\nWriteRegStr HKCU \"%s\" \"pv\" \"%s\"\n", keyPath, test.newVersion)
			}
			fixture := fmt.Sprintf(`Unicode true
Name "Q Studio Bootstrap Fixture"
OutFile "%s"
SilentInstall silent
RequestExecutionLevel user
Section
%s
FileOpen $0 "%s" w
FileWrite $0 "ran"
FileClose $0
SetErrorLevel %d
SectionEnd
`, nsisLiteral(bootstrap), register, nsisLiteral(marker), test.exit)
			compileNSIS(t, nsis, filepath.Join(dir, "bootstrap.nsi"), fixture)
			harness := fmt.Sprintf(`Unicode true
Name "Q Studio Installer Regression"
OutFile "%s"
SilentInstall silent
RequestExecutionLevel user
!define BOOTSTRAP "%s"
!define WEBVIEW_CLIENT_KEY "%s"
!include "%s"
Section
Call EnsureWebView2
FileOpen $0 "%s" w
FileWrite $0 "ready"
FileClose $0
SectionEnd
`, nsisLiteral(filepath.Join(dir, "harness.exe")), nsisLiteral(bootstrap), keyPath, nsisLiteral(include), nsisLiteral(passed))
			compileNSIS(t, nsis, filepath.Join(dir, "harness.nsi"), harness)
			command := exec.CommandContext(t.Context(), filepath.Join(dir, "harness.exe"), "/S")
			err := command.Run()
			if (err == nil) != test.success {
				t.Fatalf("installer admission: %v; wanted success=%v", err, test.success)
			}
			_, passedErr := os.Stat(passed)
			if (passedErr == nil) != test.success {
				t.Fatal("installer continued with the wrong availability state")
			}
			_, markerErr := os.Stat(marker)
			if (markerErr == nil) != test.bootstrap {
				t.Fatalf("bootstrap executed=%v; wanted %v", markerErr == nil, test.bootstrap)
			}
		})
	}
}

func compileNSIS(t *testing.T, nsis, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.CommandContext(t.Context(), nsis, "/V2", path).CombinedOutput()
	if err != nil {
		t.Fatalf("compile installer regression: %v\n%s", err, output)
	}
}

func nsisLiteral(value string) string {
	return strings.NewReplacer("$", "$$", "\"", "$\\\"").Replace(value)
}

func runtimeRegistered() bool {
	const path = `Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		for _, view := range []uint32{registry.WOW64_32KEY, registry.WOW64_64KEY} {
			key, err := registry.OpenKey(root, path, registry.QUERY_VALUE|view)
			if err != nil {
				continue
			}
			version, _, err := key.GetStringValue("pv")
			_ = key.Close()
			if err == nil && version != "" && version != "0.0.0.0" {
				return true
			}
		}
	}
	return false
}
