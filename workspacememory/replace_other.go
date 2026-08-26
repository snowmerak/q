//go:build !windows

package workspacememory

import "os"

func replaceFile(source, destination string) error { return os.Rename(source, destination) }
