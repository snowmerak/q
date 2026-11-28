//go:build windows

package fsutil

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

const (
	maximumReplaceRetries = 7

	errorSharingViolation       syscall.Errno = 32
	errorLockViolation          syscall.Errno = 33
	errorUnableToRemoveReplaced syscall.Errno = 1175
)

func replaceFile(source, destination string) error {
	sourcePtr, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destPtr, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	_, statErr := os.Stat(destination)
	destExists := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	kernel := syscall.NewLazyDLL("kernel32.dll")
	if destExists {
		proc := kernel.NewProc("ReplaceFileW")
		return retryReplace(func() error {
			res, _, callErr := proc.Call(
				uintptr(unsafe.Pointer(destPtr)),
				uintptr(unsafe.Pointer(sourcePtr)),
				0, 0, 0, 0,
			)
			if res == 0 {
				return fmt.Errorf("ReplaceFileW %s: %w", destination, callErr)
			}
			return nil
		})
	}
	const moveFileWriteThrough = 0x8
	proc := kernel.NewProc("MoveFileExW")
	return retryReplace(func() error {
		res, _, callErr := proc.Call(
			uintptr(unsafe.Pointer(sourcePtr)),
			uintptr(unsafe.Pointer(destPtr)),
			moveFileWriteThrough,
		)
		if res == 0 {
			return fmt.Errorf("MoveFileExW %s: %w", destination, callErr)
		}
		return nil
	})
}

func retryReplace(op func() error) error {
	err := op()
	for attempt := 0; attempt < maximumReplaceRetries && retryableReplaceErr(err); attempt++ {
		time.Sleep(10 * time.Millisecond * time.Duration(1<<attempt))
		err = op()
	}
	return err
}

func retryableReplaceErr(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
		errors.Is(err, errorSharingViolation) ||
		errors.Is(err, errorLockViolation) ||
		errors.Is(err, errorUnableToRemoveReplaced)
}
