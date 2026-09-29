//go:build !windows

// Package fsopen opens files with sharing semantics that remain compatible
// with atomic replacement on the host operating system.
package fsopen

import "os"

// Open opens path for reading.
func Open(path string) (*os.File, error) {
	return os.Open(path)
}
