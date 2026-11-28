//go:build unix

package cli

import (
	"os/exec"
	"syscall"
)

func backgroundProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func browserProcess(cmd *exec.Cmd, _ bool) { backgroundProcess(cmd) }
