//go:build !windows

package lsp

import "os/exec"

func configureChildProcess(_ *exec.Cmd) {}
