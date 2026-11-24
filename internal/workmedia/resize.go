package workmedia

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"slices"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	DefaultMaxEdge  = 2048
	CompactMaxEdge  = 1600
	patchSize       = 32
	maxPatches      = 2500
	maxDecodeEdge   = 16_384
	maxDecodePixels = 40_000_000
)

// ValidateMaxEdge checks the optional image resolution preset.
func ValidateMaxEdge(edge int) error {
	if edge != 0 && edge != DefaultMaxEdge && edge != CompactMaxEdge {
		return fmt.Errorf("image_max_edge must be %d or %d", DefaultMaxEdge, CompactMaxEdge)
	}
	return nil
}

// Prepare fits new attachments within the resolution and payload limits.
func Prepare(images []Image, edge int) ([]Image, error) {
	if err := ValidateMaxEdge(edge); err != nil {
		return nil, err
	}
	if err := Validate(images); err != nil {
		return nil, err
	}
	if len(images) == 0 {
		return nil, nil
	}
	remaining := MaxTotalBytes
	for _, img := range images {
		remaining -= len(img.Data)
	}
	prepared := make([]Image, len(images))
	for index, img := range images {
		var err error
		prepared[index], err = Resize(img, edge, min(MaxImageBytes, len(img.Data)+remaining))
		if err != nil {
			return nil, fmt.Errorf("prepare image %d: %w", index+1, err)
		}
		remaining += len(img.Data) - len(prepared[index].Data)
	}
	return prepared, nil
}

// Resize preserves the full image and scales both dimensions together. The
// caller owns input format and byte admission for its transport.
func Resize(input Image, edge, maxBytes int) (Image, error) {
	if edge == 0 {
		edge = DefaultMaxEdge
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(input.Data))
	if err != nil {
		return Image{}, fmt.Errorf("read image dimensions: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 {
		return Image{}, errors.New("image has invalid dimensions")
	}
	target := fitImage(config.Width, config.Height, edge)
	if target.Dx() == config.Width && target.Dy() == config.Height && len(input.Data) <= maxBytes {
		return input, nil
	}
	if int64(config.Width) > maxDecodePixels/int64(config.Height) {
		return Image{}, fmt.Errorf("image exceeds %d pixels and cannot be resized", maxDecodePixels)
	}
	if max(config.Width, config.Height) > maxDecodeEdge {
		return Image{}, fmt.Errorf("image exceeds %d pixels on its longest edge and cannot be resized", maxDecodeEdge)
	}
	source, _, err := image.Decode(bytes.NewReader(input.Data))
	if err != nil {
		return Image{}, fmt.Errorf("decode image for resizing: %w", err)
	}
	var exif []byte
	if format == "jpeg" {
		exif = jpegEXIF(input.Data)
	}
	for {
		scaled := image.NewNRGBA(target)
		frame := target
		if format == "gif" {
			bounds := source.Bounds()
			frame = image.Rect(bounds.Min.X*target.Dx()/config.Width, bounds.Min.Y*target.Dy()/config.Height,
				(bounds.Max.X*target.Dx()+config.Width-1)/config.Width, (bounds.Max.Y*target.Dy()+config.Height-1)/config.Height)
		}
		draw.CatmullRom.Scale(scaled, frame, source, source.Bounds(), draw.Src, nil)
		var encoded bytes.Buffer
		if format == "jpeg" {
			err = jpeg.Encode(&encoded, scaled, &jpeg.Options{Quality: 92})
		} else {
			err = png.Encode(&encoded, scaled)
		}
		if err != nil {
			return Image{}, fmt.Errorf("encode resized image: %w", err)
		}
		if encoded.Len()+len(exif) <= maxBytes {
			data := encoded.Bytes()
			if len(exif) > 0 {
				data = slices.Concat(data[:2], exif, data[2:])
			}
			return Image{Data: data}, nil
		}
		longEdge := max(target.Dx(), target.Dy())
		if longEdge == 1 {
			return Image{}, errors.New("resized image exceeds the available payload size")
		}
		target = fitImage(config.Width, config.Height, max(1, longEdge*4/5))
	}
}

func fitImage(width, height, edge int) image.Rectangle {
	longEdge := max(width, height)
	low, high := 1, min(longEdge, edge)
	for low < high {
		candidate := (low + high + 1) / 2
		if patchCount(scaleImage(width, height, longEdge, candidate)) <= maxPatches {
			low = candidate
		} else {
			high = candidate - 1
		}
	}
	return scaleImage(width, height, longEdge, low)
}

func scaleImage(width, height, longEdge, target int) image.Rectangle {
	scale := func(value int) int {
		return max(1, int((int64(value)*int64(target)+int64(longEdge)/2)/int64(longEdge)))
	}
	return image.Rect(0, 0, scale(width), scale(height))
}

func patchCount(bounds image.Rectangle) int {
	return ((bounds.Dx() + patchSize - 1) / patchSize) * ((bounds.Dy() + patchSize - 1) / patchSize)
}

// Keep EXIF orientation on resized JPEGs so portrait photos display correctly.
func jpegEXIF(data []byte) []byte {
	for offset := 2; offset+4 <= len(data) && data[offset] == 0xff; {
		marker := data[offset+1]
		if marker == 0xff {
			offset++
			continue
		}
		if marker == 0xda || marker == 0xd9 {
			break
		}
		size := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		end := offset + 2 + size
		if size < 2 || end > len(data) {
			break
		}
		if marker == 0xe1 && bytes.HasPrefix(data[offset+4:end], []byte("Exif\x00\x00")) {
			return data[offset:end]
		}
		offset = end
	}
	return nil
}
