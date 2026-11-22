//go:build !windows

package main

import (
	"io"
	"os/exec"
)

func configureEngine(*exec.Cmd)                  {}
func containEngine(*exec.Cmd) (io.Closer, error) { return nil, nil }
