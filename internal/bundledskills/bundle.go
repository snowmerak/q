// Package bundledskills carries the browser skill paired with Q's CDP module.
package bundledskills

import (
	"embed"
	"io/fs"
	"strings"

	"github.com/snowmerak/q/internal/skillbundle"
)

//go:generate go run ./generate
//go:embed cdp
var resources embed.FS

func CDP() skillbundle.Bundle {
	bundle := skillbundle.Bundle{Version: "20261107143548.0002", Files: map[string]string{}}
	err := fs.WalkDir(resources, "cdp", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := resources.ReadFile(path)
		if err != nil {
			return err
		}
		// Older running Library processes validate uploaded bundles too.
		// Send canonical bytes as well as computing a canonical manifest.
		bundle.Files[strings.TrimPrefix(path, "cdp/")] = strings.ReplaceAll(string(data), "\r\n", "\n")
		return nil
	})
	if err != nil {
		panic(err)
	} // Embedded files are validated at build time.
	return bundle
}
