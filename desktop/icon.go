package main

import _ "embed"

//go:embed assets/icon.png
var embeddedIconPNG []byte

// appIconBytes returns the embedded 64x64 PNG icon used by Wails windows and tray menus.
func appIconBytes() []byte {
	return embeddedIconPNG
}
