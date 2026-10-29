// Package acpadapter discovers ACP presets and installs their npm adapters in
// Q's user cache without changing the global npm prefix or the workspace.
package acpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/snowmerak/q/internal/fsreplace"
)

type Command struct {
	Name string
	Args []string
}

type Status struct {
	Preset     string `json:"preset"`
	Name       string `json:"name"`
	Package    string `json:"package,omitempty"`
	Installed  bool   `json:"installed"`
	CanInstall bool   `json:"can_install"`
	Command    string `json:"command,omitempty"`
	Version    string `json:"version,omitempty"`
	Detail     string `json:"detail"`
}

type Manager struct{ Dir string }

type definition struct{ name, binary, pkg string }

func presetDefinition(preset string) (definition, error) {
	switch preset {
	case "codex":
		return definition{"Codex", "codex-acp", "@agentclientprotocol/codex-acp"}, nil
	case "claude":
		return definition{"Claude Code", "claude-agent-acp", "@agentclientprotocol/claude-agent-acp"}, nil
	case "grok":
		return definition{"Grok", "grok", ""}, nil
	default:
		return definition{}, fmt.Errorf("unsupported ACP preset %q", preset)
	}
}

func DefaultManager() (Manager, error) {
	directory, err := os.UserCacheDir()
	if err != nil {
		return Manager{}, err
	}
	return Manager{Dir: filepath.Join(directory, "q", "acp-adapters")}, nil
}

// Resolve only returns installed commands. Callers can install a missing
// adapter when opening a connection or offer installation ahead of time.
func (m Manager) Resolve(preset string, lookPath func(string) (string, error)) (Command, string, error) {
	d, err := presetDefinition(preset)
	if err != nil {
		return Command{}, "", err
	}
	if path, err := lookPath(d.binary); err == nil {
		if d.pkg != "" && (strings.EqualFold(filepath.Ext(path), ".cmd") || strings.EqualFold(filepath.Ext(path), ".ps1")) {
			// npm's Windows shims cannot be exec'd directly. Run the package's
			// declared bin through Node, keeping arguments out of a shell.
			if command, version, err := packageCommand(filepath.Join(filepath.Dir(path), "node_modules", filepath.FromSlash(d.pkg)), d.binary, lookPath); err == nil {
				return command, version, nil
			}
		} else {
			args := []string(nil)
			if preset == "grok" {
				args = []string{"agent", "stdio"}
			}
			return Command{Name: path, Args: args}, "", nil
		}
	}
	if d.pkg != "" && m.Dir != "" {
		directory := filepath.Join(m.Dir, preset)
		if _, err := os.Stat(filepath.Join(directory, "ready.json")); err == nil {
			return packageCommand(filepath.Join(directory, "node_modules", filepath.FromSlash(d.pkg)), d.binary, lookPath)
		}
	}
	return Command{}, "", fmt.Errorf("%s ACP executable is not installed", d.name)
}

func packageCommand(directory, binary string, lookPath func(string) (string, error)) (Command, string, error) {
	body, err := os.ReadFile(filepath.Join(directory, "package.json"))
	if err != nil {
		return Command{}, "", err
	}
	var manifest struct {
		Version string            `json:"version"`
		Bin     map[string]string `json:"bin"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return Command{}, "", err
	}
	relative := manifest.Bin[binary]
	if relative == "" || !filepath.IsLocal(filepath.FromSlash(relative)) {
		return Command{}, "", errors.New("ACP package has no valid executable")
	}
	script := filepath.Join(directory, filepath.FromSlash(relative))
	if info, err := os.Stat(script); err != nil || !info.Mode().IsRegular() {
		return Command{}, "", errors.New("ACP package executable is missing")
	}
	node, err := lookPath("node")
	if err != nil {
		return Command{}, "", errors.New("running this ACP adapter requires Node.js")
	}
	return Command{Name: node, Args: []string{script}}, manifest.Version, nil
}

func npmCommand(lookPath func(string) (string, error)) (Command, error) {
	npm, err := lookPath("npm")
	if err != nil {
		return Command{}, errors.New("npm is required to install ACP adapters")
	}
	node, err := lookPath("node")
	if err != nil {
		return Command{}, errors.New("installing ACP adapters requires Node.js")
	}
	resolved, err := filepath.EvalSymlinks(npm)
	if err == nil && strings.EqualFold(filepath.Ext(resolved), ".js") {
		return Command{Name: node, Args: []string{resolved}}, nil
	}
	for _, path := range []string{
		filepath.Join(filepath.Dir(npm), "node_modules", "npm", "bin", "npm-cli.js"),
		filepath.Join(filepath.Dir(node), "node_modules", "npm", "bin", "npm-cli.js"),
	} {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return Command{Name: node, Args: []string{path}}, nil
		}
	}
	if !strings.EqualFold(filepath.Ext(npm), ".cmd") && !strings.EqualFold(filepath.Ext(npm), ".ps1") {
		return Command{Name: npm}, nil
	}
	return Command{}, errors.New("npm-cli.js was not found; repair the Node.js/npm installation")
}

func (m Manager) Status(preset string, lookPath func(string) (string, error)) Status {
	d, err := presetDefinition(preset)
	if err != nil {
		return Status{Preset: preset, Detail: err.Error()}
	}
	status := Status{Preset: preset, Name: d.name, Package: d.pkg}
	if command, version, err := m.Resolve(preset, lookPath); err == nil {
		status.Installed, status.Command, status.Version = true, command.Name, version
		status.Detail = "Ready"
		if preset == "grok" {
			status.Detail = "Native ACP · grok agent stdio"
		}
		return status
	} else {
		status.Detail = err.Error()
	}
	if d.pkg == "" {
		status.Detail = "Install the Grok Build CLI to use its native ACP support"
		return status
	}
	if _, err := npmCommand(lookPath); err == nil {
		status.CanInstall = true
		status.Detail = "Installs automatically on first use with npm"
	} else {
		status.Detail = err.Error()
	}
	return status
}

var installGate = make(chan struct{}, 1)

// Install is idempotent. ready.json is published only after npm
// succeeds and its executable exists, so interrupted installs cannot be used.
func (m Manager) Install(ctx context.Context, preset string) (Status, error) {
	return m.install(ctx, preset, exec.LookPath, runInstall)
}

func (m Manager) install(ctx context.Context, preset string, lookPath func(string) (string, error), run func(context.Context, Command, string) error) (Status, error) {
	d, err := presetDefinition(preset)
	if err != nil {
		return Status{}, err
	}
	if d.pkg == "" {
		return Status{}, errors.New("the Grok CLI supports ACP natively; no npm adapter is needed")
	}
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	select {
	case installGate <- struct{}{}:
		defer func() { <-installGate }()
	case <-ctx.Done():
		return Status{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	if status := m.Status(preset, lookPath); status.Installed {
		return status, nil
	}
	command, err := npmCommand(lookPath)
	if err != nil {
		return Status{}, err
	}
	directory := filepath.Join(m.Dir, preset)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Status{}, err
	}
	command.Args = append(command.Args, "install", "--prefix", directory, "--no-audit", "--no-fund", "--engine-strict", d.pkg)
	if err := run(ctx, command, directory); err != nil {
		return Status{}, err
	}
	_, version, err := packageCommand(filepath.Join(directory, "node_modules", filepath.FromSlash(d.pkg)), d.binary, lookPath)
	if err != nil {
		return Status{}, fmt.Errorf("verify installed ACP adapter: %w", err)
	}
	body, _ := json.Marshal(map[string]string{"package": d.pkg, "version": version})
	if err := fsreplace.WriteFile(filepath.Join(directory, "ready.json"), body, 0o600); err != nil {
		return Status{}, err
	}
	return m.Status(preset, lookPath), nil
}

func runInstall(ctx context.Context, command Command, directory string) error {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Dir = directory
	cmd.WaitDelay = 5 * time.Second
	ConfigureProcess(cmd)
	var output strings.Builder
	limited := &limitedOutput{writer: &output, remaining: 16 << 10}
	cmd.Stdout, cmd.Stderr = limited, limited
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npm adapter installation failed: %w\n%s", err, strings.TrimSpace(output.String()))
	}
	return nil
}

type limitedOutput struct {
	writer    io.Writer
	remaining int
}

func (w *limitedOutput) Write(body []byte) (int, error) {
	count := len(body)
	if len(body) > w.remaining {
		body = body[:w.remaining]
	}
	_, err := w.writer.Write(body)
	w.remaining -= len(body)
	return count, err
}
