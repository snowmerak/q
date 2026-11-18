package main

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"
)

// Wails only emits ICNS representations up to the input artwork's size.
// Prepare all macOS sizes from the existing logo without changing the source
// asset used by the tray or other platforms.
func stageDarwinAssets(root, stage, version string) error {
	icon := filepath.Join(stage, "assets", "icon.png")
	data, err := os.ReadFile(icon)
	if err != nil {
		return err
	}
	source, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	artwork := image.NewNRGBA(image.Rect(0, 0, 1024, 1024))
	draw.CatmullRom.Scale(artwork, artwork.Bounds(), source, source.Bounds(), draw.Src, nil)
	output, err := os.Create(icon)
	if err != nil {
		return err
	}
	if err := errors.Join(png.Encode(output, artwork), output.Close()); err != nil {
		return err
	}
	plist, err := os.ReadFile(filepath.Join(root, "desktop", "packaging", "darwin-Info.plist"))
	if err != nil {
		return err
	}
	plist = []byte(strings.ReplaceAll(string(plist), "__Q_STUDIO_VERSION__", version))
	return os.WriteFile(filepath.Join(stage, "support", "darwin-Info.plist"), plist, 0o644)
}
