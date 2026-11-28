//go:build !windows && !unix

package cli

import "os/exec"

func backgroundProcess(cmd *exec.Cmd) {}

func browserProcess(cmd *exec.Cmd, _ bool) {}
