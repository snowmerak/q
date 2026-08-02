//go:build !windows

package providerhost

import "os/exec"

func configureChildProcess(_ *exec.Cmd) {}
