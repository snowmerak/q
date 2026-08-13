//go:build windows

package library

import (
	"fmt"
	"syscall"
	"unsafe"
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
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")
	result, _, callErr := proc.Call(
		uintptr(unsafe.Pointer(src)), uintptr(unsafe.Pointer(dst)),
		moveFileReplaceExisting|moveFileWriteThrough,
	)
	if result == 0 {
		return fmt.Errorf("MoveFileExW: %w", callErr)
	}
	return nil
}
