//go:build !windows

package builtin

import (
	"os/exec"
	"syscall"
)

func commandShellDescription() string {
	return "POSIX shell (/bin/sh -lc)"
}

func shellCommand(command string) *exec.Cmd {
	cmd := exec.Command("/bin/sh", "-lc", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

func terminateCommand(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}
