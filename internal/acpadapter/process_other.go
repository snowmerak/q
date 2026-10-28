//go:build !windows

package acpadapter

import "os/exec"

func ConfigureProcess(command *exec.Cmd) {}
