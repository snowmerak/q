//go:build windows

package worklock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

const windowsLockOffset = 1 << 30

func tryLockFile(file *os.File) (bool, error) {
	overlapped := &windows.Overlapped{Offset: windowsLockOffset}
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, overlapped,
	)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}

func unlockFile(file *os.File) error {
	overlapped := &windows.Overlapped{Offset: windowsLockOffset}
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
}
