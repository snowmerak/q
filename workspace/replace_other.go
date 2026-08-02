//go:build !windows

package workspace

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
