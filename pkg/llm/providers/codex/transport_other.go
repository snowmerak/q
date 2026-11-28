//go:build !windows

package codex

import "os/exec"

func configureCommand(*exec.Cmd) {}
