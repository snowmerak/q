//go:build !windows

package remoteconfig

import "os"

func replaceFile(source, destination string) error { return os.Rename(source, destination) }
