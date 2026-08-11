//go:build !windows

package gatewayconfig

import "os"

func replaceFile(source, destination string) error { return os.Rename(source, destination) }
