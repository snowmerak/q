// Command studioicons regenerates the Windows executable resources and web
// favicon from Q Studio's source artwork. Run it with go generate ./... in desktop.
package main

import (
	"bytes"
	"fmt"
	"image/png"
	"os"
	"path/filepath"

	"github.com/tc-hib/winres"
)

func main() {
	if err := generate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate() error {
	source, err := os.ReadFile(filepath.Join("desktop", "assets", "icon.png"))
	if err != nil {
		return err
	}
	artwork, err := png.Decode(bytes.NewReader(source))
	if err != nil {
		return err
	}
	icon, err := winres.NewIconFromResizedImage(artwork, []int{256, 128, 64, 48, 32, 24, 16})
	if err != nil {
		return err
	}
	var resources winres.ResourceSet
	// Wails loads application icon group 3 for the native window.
	if err = resources.SetIcon(winres.RT_ICON, icon); err != nil {
		return err
	}
	for _, target := range []struct {
		name string
		arch winres.Arch
	}{{"amd64", winres.ArchAMD64}, {"arm64", winres.ArchARM64}} {
		var object bytes.Buffer
		if err = resources.WriteObject(&object, target.arch); err != nil {
			return err
		}
		path := filepath.Join("desktop", "icon_windows_"+target.name+".syso")
		if err = os.WriteFile(path, object.Bytes(), 0o644); err != nil {
			return err
		}
	}
	public := filepath.Join("studio", "frontend", "public")
	if err = os.MkdirAll(public, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(public, "favicon.png"), source, 0o644)
}
