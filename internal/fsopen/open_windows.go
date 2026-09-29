//go:build windows

// Package fsopen opens files with sharing semantics that remain compatible
// with atomic replacement on the host operating system.
package fsopen

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const maximumOpenRetries = 3

// Open opens path for reading while allowing an atomic writer to replace the
// directory entry. os.Open omits FILE_SHARE_DELETE on Windows, so merely
// reading a projection can otherwise make MoveFileExW fail.
func Open(path string) (*os.File, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var file *os.File
	err = retry(func() error {
		handle, openErr := windows.CreateFile(
			pathPointer,
			windows.GENERIC_READ,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil,
			windows.OPEN_EXISTING,
			windows.FILE_ATTRIBUTE_NORMAL,
			0,
		)
		if openErr != nil {
			return openErr
		}
		file = os.NewFile(uintptr(handle), path)
		if file == nil {
			_ = windows.CloseHandle(handle)
			return errors.New("open file: invalid Windows handle")
		}
		return nil
	}, time.Sleep)
	return file, err
}

func retry(operation func() error, sleep func(time.Duration)) error {
	err := operation()
	for attempt := 0; attempt < maximumOpenRetries && retryable(err); attempt++ {
		sleep(10 * time.Millisecond * time.Duration(1<<attempt))
		err = operation()
	}
	return err
}

func retryable(err error) bool {
	return errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
