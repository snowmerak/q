package acpadapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func testPackage(t *testing.T, directory, preset string) string {
	t.Helper()
	d, err := presetDefinition(preset)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(directory, "node_modules", filepath.FromSlash(d.pkg))
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"version":"1.2.3","bin":{"`+d.binary+`":"dist/index.js"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "dist", "index.js")
	if err := os.WriteFile(script, []byte("// fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	return script
}

func testPaths(t *testing.T) (map[string]string, func(string) (string, error)) {
	t.Helper()
	directory := t.TempDir()
	npm := filepath.Join(directory, "npm-cli.js")
	if err := os.WriteFile(npm, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{"node": filepath.Join(directory, "node"), "npm": npm}
	return paths, func(name string) (string, error) {
		if path, ok := paths[name]; ok {
			return path, nil
		}
		return "", errors.New("not installed")
	}
}

func TestInstallAdaptersAndResolveWithoutGlobalPATH(t *testing.T) {
	for _, preset := range []string{"codex", "claude"} {
		t.Run(preset, func(t *testing.T) {
			manager := Manager{Dir: t.TempDir()}
			paths, lookPath := testPaths(t)
			before := manager.Status(preset, lookPath)
			if before.Installed || !before.CanInstall {
				t.Fatalf("before = %+v", before)
			}
			calls := 0
			status, err := manager.install(t.Context(), preset, lookPath, func(ctx context.Context, command Command, directory string) error {
				calls++
				if command.Name != paths["node"] || !slices.Equal(command.Args, []string{paths["npm"], "install", "--prefix", directory, "--no-audit", "--no-fund", "--engine-strict", before.Package}) {
					t.Fatalf("npm command = %+v", command)
				}
				testPackage(t, directory, preset)
				return nil
			})
			if err != nil || !status.Installed || status.Version != "1.2.3" {
				t.Fatalf("installed = %+v, %v", status, err)
			}
			command, _, err := manager.Resolve(preset, lookPath)
			if err != nil || command.Name != paths["node"] || len(command.Args) != 1 || !strings.Contains(command.Args[0], "node_modules") {
				t.Fatalf("resolve = %+v, %v", command, err)
			}
			if _, err := manager.install(t.Context(), preset, lookPath, func(context.Context, Command, string) error { calls++; return nil }); err != nil || calls != 1 {
				t.Fatalf("not idempotent: calls=%d, %v", calls, err)
			}
		})
	}
}

func TestFailedInstallationIsNotPublishedAndCanRetry(t *testing.T) {
	manager := Manager{Dir: t.TempDir()}
	_, lookPath := testPaths(t)
	_, err := manager.install(t.Context(), "codex", lookPath, func(ctx context.Context, command Command, directory string) error {
		testPackage(t, directory, "codex")
		return errors.New("download interrupted")
	})
	if err == nil || manager.Status("codex", lookPath).Installed {
		t.Fatalf("partial installation was published: %v", err)
	}
	status, err := manager.install(t.Context(), "codex", lookPath, func(context.Context, Command, string) error { return nil })
	if err != nil || !status.Installed {
		t.Fatalf("retry = %+v, %v", status, err)
	}
}

func TestNativeGrokAndMissingNPM(t *testing.T) {
	manager := Manager{Dir: t.TempDir()}
	paths, lookPath := testPaths(t)
	paths["grok"] = "grok.exe"
	command, _, err := manager.Resolve("grok", lookPath)
	if err != nil || command.Name != "grok.exe" || !slices.Equal(command.Args, []string{"agent", "stdio"}) {
		t.Fatalf("Grok = %+v, %v", command, err)
	}
	delete(paths, "npm")
	if status := manager.Status("claude", lookPath); status.CanInstall || !strings.Contains(status.Detail, "npm") {
		t.Fatalf("no npm = %+v", status)
	}
	for _, preset := range []string{"grok", "../../arbitrary-package"} {
		if _, err := manager.Install(t.Context(), preset); err == nil {
			t.Fatalf("installed unsupported adapter %q", preset)
		}
	}
}

func TestWindowsNPMShimsRunWithNodeAndLiteralArguments(t *testing.T) {
	manager := Manager{Dir: t.TempDir()}
	paths, lookPath := testPaths(t)
	root := t.TempDir()
	script := testPackage(t, root, "claude")
	paths["claude-agent-acp"] = filepath.Join(root, "claude-agent-acp.cmd")
	command, _, err := manager.Resolve("claude", lookPath)
	if err != nil || command.Name != paths["node"] || !slices.Equal(command.Args, []string{script}) {
		t.Fatalf("shim = %+v, %v", command, err)
	}
	npmScript := filepath.Join(root, "node_modules", "npm", "bin", "npm-cli.js")
	if err := os.MkdirAll(filepath.Dir(npmScript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(npmScript, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	paths["npm"] = filepath.Join(root, "npm.cmd")
	command, err = npmCommand(lookPath)
	if err != nil || command.Name != paths["node"] || !slices.Equal(command.Args, []string{npmScript}) {
		t.Fatalf("npm shim = %+v, %v", command, err)
	}
}

func TestInstallCancellationWhileWaiting(t *testing.T) {
	installGate <- struct{}{}
	defer func() { <-installGate }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := (Manager{Dir: t.TempDir()}).Install(ctx, "codex")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
}
