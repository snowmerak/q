//go:build windows

// Package fsreplace replaces files with the strongest atomic semantics offered
// by the host operating system.
package fsreplace

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

const maximumReplaceRetries = 7

const (
	errorSharingViolation       syscall.Errno = 32
	errorLockViolation          syscall.Errno = 33
	errorUnableToRemoveReplaced syscall.Errno = 1175
)

// Replace moves source over destination. ReplaceFileW preserves atomic replace
// semantics while readers hold handles that share deletion. MoveFileExW covers
// the first write when no destination exists. Antivirus and indexer handles can
// briefly deny either operation, so transient sharing errors back off for at
// most 1.27 seconds. Error 1175 leaves both names intact and is safe to retry;
// errors 1176 and 1177 can change those names and must not be retried here.
func Replace(source, destination string) error {
	sourcePointer, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPointer, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	_, statErr := os.Stat(destination)
	destinationExists := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	kernel := syscall.NewLazyDLL("kernel32.dll")
	if destinationExists {
		procedure := kernel.NewProc("ReplaceFileW")
		return retry(func() error {
			result, _, callErr := procedure.Call(
				uintptr(unsafe.Pointer(destinationPointer)),
				uintptr(unsafe.Pointer(sourcePointer)),
				0, 0, 0, 0,
			)
			if result == 0 {
				return fmt.Errorf("ReplaceFileW %s: %w", destination, callErr)
			}
			return nil
		}, time.Sleep)
	}
	const moveFileWriteThrough = 0x8
	procedure := kernel.NewProc("MoveFileExW")
	return retry(func() error {
		result, _, callErr := procedure.Call(
			uintptr(unsafe.Pointer(sourcePointer)),
			uintptr(unsafe.Pointer(destinationPointer)),
			moveFileWriteThrough,
		)
		if result == 0 {
			return fmt.Errorf("MoveFileExW %s: %w", destination, callErr)
		}
		return nil
	}, time.Sleep)
}

func retry(operation func() error, sleep func(time.Duration)) error {
	err := operation()
	for attempt := 0; attempt < maximumReplaceRetries && retryable(err); attempt++ {
		sleep(10 * time.Millisecond * time.Duration(1<<attempt))
		err = operation()
	}
	return err
}

func retryable(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
		errors.Is(err, errorSharingViolation) ||
		errors.Is(err, errorLockViolation) ||
		errors.Is(err, errorUnableToRemoveReplaced)
}
