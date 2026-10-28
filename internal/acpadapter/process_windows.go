package acpadapter

import (
	"os/exec"
	"syscall"
)

func ConfigureProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
