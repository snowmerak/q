//go:build windows

package mcpconfig

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
	const flags = 0x1 | 0x8
	result, _, callErr := syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW").Call(
		uintptr(unsafe.Pointer(src)), uintptr(unsafe.Pointer(dst)), flags,
	)
	if result == 0 {
		return fmt.Errorf("MoveFileExW: %w", callErr)
	}
	return nil
}
