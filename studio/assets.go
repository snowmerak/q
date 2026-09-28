package studio

import (
	"embed"
	"io/fs"
)

// frontendFiles is versioned build output so q remains installable with only
// the Go toolchain. all: keeps framework-owned underscore directories eligible.
//
//go:embed all:frontend/dist
var frontendFiles embed.FS

func frontend() (fs.FS, error) {
	return fs.Sub(frontendFiles, "frontend/dist")
}
