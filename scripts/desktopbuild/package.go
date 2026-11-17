package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

func command(directory string, overrides map[string]string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = directory
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	env := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	env["GOWORK"] = "off"
	for key, value := range overrides {
		env[key] = value
	}
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func wailsCLI(root, explicit string) (string, error) {
	path := explicit
	if path == "" {
		name := "wails3"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		path = filepath.Join(root, "bin", "tools", name)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return "", err
			}
			if err := command(root, map[string]string{"GOBIN": filepath.Dir(path)}, "go", "install", "github.com/wailsapp/wails/v3/cmd/wails3@"+wailsVersion); err != nil {
				return "", err
			}
		}
	}
	path, err := exec.LookPath(path)
	if err != nil {
		return "", err
	}
	version, err := exec.Command(path, "version").CombinedOutput()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(version)) != wailsVersion {
		return "", fmt.Errorf("wails CLI must be %s, got %q", wailsVersion, strings.TrimSpace(string(version)))
	}
	return path, nil
}

func packageTarget(root, binaries string, t target, options buildOptions) error {
	// Wails v3.1's Windows DMG writer copies Windows permission bits into
	// HFS+, making the bundled executables non-executable. Use a Unix host.
	if runtime.GOOS == "windows" && t.os == "darwin" {
		return fmt.Errorf("macOS DMG packaging requires macOS or Linux to preserve executable permissions; use the native CI matrix")
	}
	cli, err := wailsCLI(root, options.wails)
	if err != nil {
		return err
	}
	stage := filepath.Join(root, "bin", "studio-packaging", t.os+"-"+t.arch+"-"+options.version)
	if err = stagePackage(root, stage, binaries, t, options); err != nil {
		return err
	}
	formats := options.formats
	if formats == "" {
		switch t.os {
		case "windows":
			formats = "nsis"
		case "darwin":
			formats = "dmg"
		case "linux":
			formats = "deb,rpm"
		}
	}
	env := map[string]string{"WAILS_EXP_USE_WAKE": "1"}
	if options.nsis != "" {
		nsis, err := filepath.Abs(options.nsis)
		if err != nil {
			return err
		}
		if _, err = os.Stat(nsis); err != nil {
			return err
		}
		env["PATH"] = filepath.Dir(nsis) + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	// Wails owns architecture validation, assets, manifests, app bundles,
	// companion installation and final native package creation.
	if err = command(stage, env, cli, "build", "--skip-build", "--targets", t.os+"/"+t.arch, "--formats", formats); err != nil {
		return err
	}
	return collectPackages(root, stage, t, options.version, formats)
}

func stagePackage(root, stage, binaries string, t target, options buildOptions) error {
	for _, dir := range []string{"bin", "assets", "support"} {
		if err := os.MkdirAll(filepath.Join(stage, dir), 0o755); err != nil {
			return err
		}
	}
	app, engine := t.names()
	for _, name := range []string{app, engine} {
		if err := copyFile(filepath.Join(binaries, name), filepath.Join(stage, "bin", name), 0o755); err != nil {
			return err
		}
	}
	if err := copyFile(filepath.Join(root, "desktop", "assets", "icon.png"), filepath.Join(stage, "assets", "icon.png"), 0o644); err != nil {
		return err
	}
	for _, name := range []string{"windows-wails.nsi", "webview2.nsh", "nfpm.yaml", "q-studio.desktop"} {
		if err := copyFile(filepath.Join(root, "desktop", "packaging", name), filepath.Join(stage, "support", name), 0o644); err != nil {
			return err
		}
	}
	if err := stageLinuxConfig(stage, t, options.version); err != nil {
		return err
	}
	// Wails intentionally disallows mixing complete template replacement with
	// structured NSIS defines. Inline our runtime check into the staged template
	// while retaining Wails' generated metadata and companion wrapper.
	scriptPath := filepath.Join(stage, "support", "windows-wails.nsi")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		return err
	}
	runtimeCheck, err := os.ReadFile(filepath.Join(stage, "support", "webview2.nsh"))
	if err != nil {
		return err
	}
	script = []byte(strings.ReplaceAll(string(script), `!include "__Q_STUDIO_WEBVIEW2__"`, string(runtimeCheck)))
	if err = os.WriteFile(scriptPath, script, 0o644); err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(root, "desktop", "packaging", "wails.yaml"))
	if err != nil {
		return err
	}
	var manifest map[string]any
	if err = yaml.Unmarshal(body, &manifest); err != nil {
		return err
	}
	project := manifest["project"].(map[string]any)
	project["version"] = options.version
	project["binary_name"] = strings.TrimSuffix(app, ".exe")
	manifest["targets"] = map[string]any{t.os + "/" + t.arch: map[string]any{}}
	// Wails uses the first field to distinguish its native manifest from legacy
	// metadata, so preserve the version marker ahead of the sorted YAML map.
	delete(manifest, "version")
	body, err = yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stage, "wails.yaml"), append([]byte("version: 3\n"), body...), 0o644)
}

func stageLinuxConfig(stage string, t target, version string) error {
	path := filepath.Join(stage, "support", "nfpm.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var config map[string]any
	if err = yaml.Unmarshal(body, &config); err != nil {
		return err
	}
	config["version"] = version
	config["arch"] = t.arch
	for _, item := range config["contents"].([]any) {
		entry := item.(map[string]any)
		if entry["type"] == "symlink" {
			continue
		}
		source := entry["src"].(string)
		switch {
		case strings.HasSuffix(source, "appicon.png"):
			entry["src"] = filepath.Join(stage, "assets", "icon.png")
		case strings.HasSuffix(source, ".desktop"):
			entry["src"] = filepath.Join(stage, "support", "q-studio.desktop")
		default:
			entry["src"] = filepath.Join(stage, filepath.FromSlash(source))
		}
	}
	body, err = yaml.Marshal(config)
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}

func collectPackages(root, stage string, t target, version, formats string) error {
	prefix := filepath.Join(root, "bin", fmt.Sprintf("Q-Studio-%s-%s-%s", version, t.os, t.arch))
	requested := map[string]bool{}
	for _, format := range strings.Split(formats, ",") {
		requested[format] = true
	}
	count := 0
	err := filepath.WalkDir(filepath.Join(stage, "bin"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if strings.HasSuffix(entry.Name(), ".app") {
				out := prefix + ".app.zip"
				if err := zipDirectory(path, out, true); err != nil {
					return err
				}
				fmt.Println("Application bundle:", out)
				count++
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		format := strings.TrimPrefix(ext, ".")
		if ext == ".exe" && strings.Contains(entry.Name(), "installer") {
			format = "nsis"
		}
		if !requested[format] {
			return nil
		}
		out := prefix + ext
		if ext == ".exe" {
			out = prefix + "-setup.exe"
		}
		if err := copyFile(path, out, 0o644); err != nil {
			return err
		}
		fmt.Println("Installer:", out)
		count++
		return nil
	})
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("wails did not produce installable artifacts")
	}
	return nil
}

func copyFile(source, destination string, mode fs.FileMode) (returnErr error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, input.Close()) }()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, input)
	return errors.Join(err, output.Close())
}

func zipDirectory(directory, path string, includeRoot bool, only ...string) (returnErr error) {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	archive := zip.NewWriter(file)
	allowed := map[string]bool{}
	for _, name := range only {
		allowed[filepath.ToSlash(name)] = true
	}
	err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		if len(allowed) > 0 && !allowed[filepath.ToSlash(relative)] {
			return nil
		}
		if includeRoot {
			relative = filepath.Join(filepath.Base(directory), relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		header.Method = zip.Deflate
		if len(allowed) > 0 {
			// Explicit entries are the compiled executable pair. Preserve Unix
			// launchability even when the archive is built on Windows.
			header.SetMode(0o755)
		}
		writer, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(writer, input)
		return errors.Join(err, input.Close())
	})
	return errors.Join(err, archive.Close())
}
