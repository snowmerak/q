//go:build windows

package providerenv

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// userEnvironment reads the current user's environment as a new logon would
// see it, from the registry's user and system variables. Inheritance is
// disabled so values inherited by this process do not mask the update.
func userEnvironment() (map[string]string, error) {
	var token windows.Token
	access := uint32(windows.TOKEN_QUERY | windows.TOKEN_DUPLICATE | windows.TOKEN_IMPERSONATE)
	if err := windows.OpenProcessToken(windows.CurrentProcess(), access, &token); err != nil {
		return nil, fmt.Errorf("providerenv: open process token: %w", err)
	}
	defer func() { _ = token.Close() }()
	var block *uint16
	if err := windows.CreateEnvironmentBlock(&block, token, false); err != nil {
		return nil, fmt.Errorf("providerenv: create environment block: %w", err)
	}
	defer func() { _ = windows.DestroyEnvironmentBlock(block) }()
	return parseEnvironmentBlock(block), nil
}

// parseEnvironmentBlock reads a sequence of NUL-terminated UTF-16 strings
// that ends with an empty string.
func parseEnvironmentBlock(block *uint16) map[string]string {
	result := make(map[string]string)
	pointer := unsafe.Pointer(block)
	for {
		length := 0
		for *(*uint16)(unsafe.Add(pointer, length*2)) != 0 {
			length++
		}
		if length == 0 {
			return result
		}
		entry := windows.UTF16ToString(unsafe.Slice((*uint16)(pointer), length))
		pointer = unsafe.Add(pointer, (length+1)*2)
		// Entries such as "=C:=C:\\" hold per-drive directories, not variables.
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		result[name] = value
	}
}
