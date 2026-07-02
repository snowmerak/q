package main

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
)

type SystemInfo struct {
	OS       string
	Shell    string
	Username string
	PWD      string
}

func GetSystemInfo() *SystemInfo {
	info := &SystemInfo{
		OS: runtime.GOOS,
	}

	if wd, err := os.Getwd(); err == nil {
		info.PWD = wd
	} else {
		info.PWD = "."
	}

	if u, err := user.Current(); err == nil {
		info.Username = u.Username
	} else {
		info.Username = os.Getenv("USER")
		if info.Username == "" {
			info.Username = os.Getenv("USERNAME")
		}
	}

	shellPath := os.Getenv("SHELL")
	if shellPath != "" {
		info.Shell = filepath.Base(shellPath)
	} else {
		if runtime.GOOS == "windows" {
			if os.Getenv("PSModulePath") != "" {
				info.Shell = "powershell"
			} else {
				info.Shell = "cmd"
			}
		} else {
			info.Shell = "sh"
		}
	}

	return info
}
