//go:build !windows

package library

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
