//go:build !windows

package mcpconfig

import "os"

func replaceFile(source, destination string) error { return os.Rename(source, destination) }
