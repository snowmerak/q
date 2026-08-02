//go:build !windows

package providerhost

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
