//go:build !windows && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package loom

import (
	"errors"
	"os"
)

func tryOperationFileLock(*os.File) (bool, error) {
	return false, errors.New("loom: operation locking is not supported on this platform")
}

func unlockOperationFile(*os.File) error { return nil }
