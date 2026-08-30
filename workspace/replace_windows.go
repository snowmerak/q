//go:build windows

package workspace

import (
	"errors"
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

const maximumReplaceRetries = 3

const (
	errorSharingViolation syscall.Errno = 32
	errorLockViolation    syscall.Errno = 33
)

func replaceFile(source, destination string) error {
	src, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	dst, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	const (
		moveFileReplaceExisting = 0x1
		moveFileWriteThrough    = 0x8
	)
	procedure := syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")
	return retryReplaceFile(func() error {
		result, _, callErr := procedure.Call(
			uintptr(unsafe.Pointer(src)),
			uintptr(unsafe.Pointer(dst)),
			moveFileReplaceExisting|moveFileWriteThrough,
		)
		if result == 0 {
			return fmt.Errorf("MoveFileExW: %w", callErr)
		}
		return nil
	}, time.Sleep)
}

func retryReplaceFile(operation func() error, sleep func(time.Duration)) error {
	err := operation()
	for retry := 0; retry < maximumReplaceRetries && isRetryableReplaceError(err); retry++ {
		sleep(10 * time.Millisecond * time.Duration(1<<retry))
		err = operation()
	}
	return err
}

func isRetryableReplaceError(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
		errors.Is(err, errorSharingViolation) ||
		errors.Is(err, errorLockViolation)
}
