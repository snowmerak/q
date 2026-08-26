//go:build windows

package loom

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

const windowsOperationLockOffset = 1 << 30

func tryOperationFileLock(file *os.File) (bool, error) {
	overlapped := &windows.Overlapped{Offset: windowsOperationLockOffset}
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

func unlockOperationFile(file *os.File) error {
	overlapped := &windows.Overlapped{Offset: windowsOperationLockOffset}
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
}
