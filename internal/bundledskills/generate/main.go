// Command generate refreshes the skill from the pinned module, never from HEAD.
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	data, err := exec.Command("go", "list", "-m", "-json", "github.com/snowmerak/cdp-q-driver").Output()
	if err != nil {
		return err
	}
	var module struct{ Dir, Version string }
	if err := json.Unmarshal(data, &module); err != nil {
		return err
	}
	source := filepath.Join(module.Dir, "skills", "cdp-q-driver")
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join("cdp", relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := strings.ReplaceAll(string(body), "\r\n", "\n")
		if relative == "SKILL.md" {
			start := strings.Index(text, "The CLI executable and an installed")
			end := strings.Index(text, "1. Reuse a user-selected session.")
			if start < 0 || end < start {
				return fmt.Errorf("upstream skill introduction changed")
			}
			text = text[:start] + "Q includes this driver as `q cdp`; a separate driver installation is unnecessary. Use the executable and arguments from Q's browser preference, or `q cdp` on PATH. Run `q cdp help` for the command contract. An installed Chromium browser is required for browser operations; Node.js is required only for `run`.\n\n" + text[end:]
		}
		text = strings.ReplaceAll(text, "cdp-q-driver", "q cdp")
		text = strings.ReplaceAll(text, "name: q cdp\n", "name: q-cdp\n")
		text += "\n<!-- Bundled from github.com/snowmerak/cdp-q-driver " + module.Version + "; refresh with go generate ./internal/bundledskills. -->\n"
		return os.WriteFile(target, []byte(text), 0o644)
	})
}
