//go:build !windows

package studioprocess

import "os"

func secureDirectory(path string) error { return os.Chmod(path, 0o700) }
