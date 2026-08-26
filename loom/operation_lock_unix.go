//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package loom

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryOperationFileLock(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}

func unlockOperationFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
