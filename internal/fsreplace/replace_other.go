//go:build !windows

// Package fsreplace replaces files with the strongest atomic semantics offered
// by the host operating system.
package fsreplace

import "os"

// Replace moves source over destination.
func Replace(source, destination string) error {
	return os.Rename(source, destination)
}
