//go:build !windows

package sessionstore

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
