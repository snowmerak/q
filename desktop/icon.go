package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// appIconBytes generates a deterministic 32x32 PNG icon representing Q v2
// so desktop windows and the system tray always have valid image bytes without
// external asset files.
func appIconBytes() []byte {
	const size = 32
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	bg := color.RGBA{R: 17, G: 24, B: 39, A: 255}
	ring := color.RGBA{R: 248, G: 250, B: 252, A: 255}
	accent := color.RGBA{R: 56, G: 189, B: 248, A: 255}

	cx, cy := 15.5, 15.0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetRGBA(x, y, bg)
			dx := float64(x) - cx
			dy := float64(y) - cy
			dist := math.Hypot(dx, dy)
			if dist >= 6.5 && dist <= 10.5 {
				img.SetRGBA(x, y, ring)
			}
			// Q tail
			if x >= 17 && x <= 25 && y >= 17 && y <= 26 && math.Abs(float64(x-y)) <= 2.2 {
				img.SetRGBA(x, y, accent)
			}
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
