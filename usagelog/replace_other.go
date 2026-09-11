//go:build !windows

package usagelog

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
