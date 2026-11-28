//go:build !windows

package qdriver_test

import "os/exec"

func hideWindow(_ *exec.Cmd) {}
