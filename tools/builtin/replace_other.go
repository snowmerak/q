//go:build !windows

package builtin

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
