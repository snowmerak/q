package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTargetValidation(t *testing.T) {
	for _, value := range []string{"../windows/arm64", "linux/../../tmp", "windows/386", "ios/arm64", "linux"} {
		if _, err := parseTargets(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	got, err := parseTargets("windows/arm64,linux/amd64,darwin/arm64,windows/arm64")
	if err != nil || len(got) != 3 {
		t.Fatalf("targets: %v %v", got, err)
	}
}

func TestPackageStagingPreservesEngineAndInstallContracts(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []target{{"windows", "arm64"}, {"linux", "amd64"}, {"darwin", "arm64"}} {
		t.Run(target.os, func(t *testing.T) {
			dir := t.TempDir()
			binaries := filepath.Join(dir, "compiled")
			stage := filepath.Join(dir, "staged")
			if err = os.MkdirAll(binaries, 0o755); err != nil {
				t.Fatal(err)
			}
			app, engine := target.names()
			for _, name := range []string{app, engine} {
				if err = os.WriteFile(filepath.Join(binaries, name), []byte("compiled "+name), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err = stagePackage(root, stage, binaries, target, buildOptions{version: "2.3.4"}); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(stage, "wails.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(body), "version: 3\n") {
				t.Fatal("Wails manifest version marker must be first")
			}
			var manifest map[string]any
			if err = yaml.Unmarshal(body, &manifest); err != nil {
				t.Fatal(err)
			}
			project := manifest["project"].(map[string]any)
			if project["version"] != "2.3.4" || project["binary_name"] != strings.TrimSuffix(app, ".exe") {
				t.Fatal("package metadata does not match target")
			}
			for _, name := range []string{app, engine} {
				copied, err := os.ReadFile(filepath.Join(stage, "bin", name))
				if err != nil || string(copied) != "compiled "+name {
					t.Fatalf("wrong companion bytes: %s %v", name, err)
				}
			}
			body, err = os.ReadFile(filepath.Join(stage, "support", "nfpm.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]any
			if err = yaml.Unmarshal(body, &config); err != nil {
				t.Fatal(err)
			}
			if config["arch"] != target.arch || config["version"] != "2.3.4" {
				t.Fatal("Linux package version or architecture is stale")
			}
			var appFound, engineFound bool
			for _, raw := range config["contents"].([]any) {
				entry := raw.(map[string]any)
				if entry["type"] == "symlink" {
					continue
				}
				source := entry["src"].(string)
				if _, err = os.Stat(source); err != nil && target.os == "linux" {
					t.Errorf("missing package input %s", source)
				}
				switch entry["dst"] {
				case "/usr/lib/q-studio/q-studio":
					appFound = true
				case "/usr/lib/q-studio/q-engine":
					engineFound = true
				case "/usr/bin/q":
					t.Fatal("desktop package overwrites ordinary q")
				}
			}
			if !appFound || !engineFound {
				t.Fatal("Linux package does not contain paired private executables")
			}
			body, err = os.ReadFile(filepath.Join(stage, "support", "windows-wails.nsi"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "__Q_STUDIO_WEBVIEW2__") || !strings.Contains(string(body), "Function EnsureWebView2") {
				t.Fatal("Windows installer lost runtime check")
			}
		})
	}
}

func TestAppArchiveRetainsBundleRootAndExecutableMode(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "Q Studio.app")
	binary := filepath.Join(bundle, "Contents", "MacOS", "Q Studio")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("native binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "app.zip")
	if err := zipDirectory(bundle, archive, true); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if len(reader.File) != 1 || reader.File[0].Name != "Q Studio.app/Contents/MacOS/Q Studio" {
		t.Fatal("archive removed the .app root")
	}
	// Windows does not preserve Unix executable bits on disk; native Unix CI
	// additionally exercises packaging with its real executable permissions.
	if info, err := os.Stat(binary); err != nil || reader.File[0].Mode() != info.Mode() {
		t.Fatal("archive changed file permissions")
	}
}

func TestPortableArchiveExcludesOldOrUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Q Studio.exe", "q-engine.exe", "q.exe", "settings.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(t.TempDir(), "portable.zip")
	if err := zipDirectory(dir, archive, false, "Q Studio.exe", "q-engine.exe"); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if len(reader.File) != 2 {
		t.Fatal("portable archive contains stale build outputs")
	}
	for _, file := range reader.File {
		if file.Name != "Q Studio.exe" && file.Name != "q-engine.exe" {
			t.Fatalf("unexpected distribution file %s", file.Name)
		}
		if file.Mode().Perm() != 0o755 {
			t.Fatalf("distribution executable lost launch permissions: %s", file.Name)
		}
	}
}
