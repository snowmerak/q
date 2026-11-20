// Command desktopbuild compiles Q's independent modules and delegates native
// package assembly to the pinned Wails CLI. It never assembles a DMG/DEB itself.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const wailsVersion = "v3.1.0"

type buildOptions struct {
	version, targets, formats, nsis, wails string
	installer, purego, packageOnly         bool
}

type target struct{ os, arch string }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var options buildOptions
	flag.StringVar(&options.version, "version", "0.1.2", "product version (major.minor.patch)")
	flag.StringVar(&options.targets, "targets", runtime.GOOS+"/"+runtime.GOARCH, "comma-separated OS/architecture targets")
	flag.StringVar(&options.formats, "formats", "", "package formats (default: Windows nsis; macOS dmg; Linux deb,rpm)")
	flag.StringVar(&options.nsis, "nsis", "", "optional NSIS compiler path")
	flag.StringVar(&options.wails, "wails", "", "optional path to Wails CLI "+wailsVersion)
	flag.BoolVar(&options.installer, "installer", false, "create installable packages using Wails")
	flag.BoolVar(&options.purego, "purego", false, "explicitly use Wails' experimental purego backend for macOS/Linux cross-builds")
	flag.BoolVar(&options.packageOnly, "package-only", false, "package existing paired binaries without compiling")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flag.Args())
	}
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(options.version) {
		return fmt.Errorf("version must be major.minor.patch")
	}
	targets, err := parseTargets(options.targets)
	if err != nil {
		return err
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err = os.Stat(filepath.Join(root, "desktop", "go.mod")); err != nil {
		return fmt.Errorf("run from the Q repository root: %w", err)
	}
	for _, target := range targets {
		if err = buildTarget(root, target, options); err != nil {
			return fmt.Errorf("%s/%s: %w", target.os, target.arch, err)
		}
	}
	return nil
}

func parseTargets(value string) ([]target, error) {
	var targets []target
	seen := map[string]bool{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		parts := strings.Split(item, "/")
		if len(parts) != 2 || (parts[0] != "windows" && parts[0] != "linux" && parts[0] != "darwin") || (parts[1] != "amd64" && parts[1] != "arm64") {
			return nil, fmt.Errorf("unsupported target %q; use windows, linux or darwin with amd64 or arm64", item)
		}
		if !seen[item] {
			targets = append(targets, target{parts[0], parts[1]})
			seen[item] = true
		}
	}
	return targets, nil
}

func (t target) names() (string, string) {
	switch t.os {
	case "windows":
		return "Q Studio.exe", "q-engine.exe"
	case "darwin":
		return "Q Studio", "q-engine"
	default:
		return "q-studio", "q-engine"
	}
}

func buildTarget(root string, t target, options buildOptions) error {
	directory := filepath.Join(root, "bin", "studio", t.os+"-"+t.arch)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	app, engine := t.names()
	if !options.packageOnly {
		if t.os != "windows" && (t.os != runtime.GOOS || t.arch != runtime.GOARCH) && !options.purego {
			return fmt.Errorf("native GUI build requires a %s/%s build host; use the native CI matrix or explicitly opt in with -purego", t.os, t.arch)
		}
		env := map[string]string{"GOOS": t.os, "GOARCH": t.arch, "CGO_ENABLED": "0"}
		if err := command(root, env, "go", "build", "-trimpath", "-o", filepath.Join(directory, engine), "./cmd/q"); err != nil {
			return err
		}
		tags, ldflags := "production", "-s -w"
		if t.os == "windows" {
			ldflags += " -H=windowsgui"
		} else if options.purego {
			tags += ",purego"
		} else {
			env["CGO_ENABLED"] = "1"
		}
		if err := command(filepath.Join(root, "desktop"), env, "go", "build", "-trimpath", "-tags", tags, "-ldflags", ldflags, "-o", filepath.Join(directory, app), "."); err != nil {
			return err
		}
	}
	for _, name := range []string{app, engine} {
		if info, err := os.Stat(filepath.Join(directory, name)); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("missing paired executable %s", name)
		}
	}
	if t.os == "darwin" && (options.installer || options.packageOnly) {
		return packageTarget(root, directory, t, options)
	}
	archive := filepath.Join(root, "bin", fmt.Sprintf("Q-Studio-%s-%s-%s.zip", options.version, t.os, t.arch))
	if err := zipDirectory(directory, archive, false, app, engine); err != nil {
		return err
	}
	fmt.Println("Portable archive:", archive)
	if options.installer || options.packageOnly {
		return packageTarget(root, directory, t, options)
	}
	return nil
}
