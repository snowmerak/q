package workmedia

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestPrepareResizesToEdgeAndPatchBudget(t *testing.T) {
	for _, test := range []struct {
		name          string
		width, height int
		edge          int
		wantW, wantH  int
	}{
		{"standard wide", 3420, 1312, 0, 2048, 786},
		{"standard square", 2048, 2048, DefaultMaxEdge, 1600, 1600},
		{"standard in bounds", 1888, 1056, DefaultMaxEdge, 1888, 1056},
		{"smaller wide", 1888, 1056, CompactMaxEdge, 1600, 895},
		{"smaller portrait", 1056, 1888, CompactMaxEdge, 895, 1600},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := testPNG(t, test.width, test.height)
			prepared, err := Prepare([]Image{input}, test.edge)
			if err != nil {
				t.Fatal(err)
			}
			data := prepared[0].Data
			config, err := png.DecodeConfig(bytes.NewReader(data))
			if err != nil || config.Width != test.wantW || config.Height != test.wantH {
				t.Fatalf("prepared %dx%d, want %dx%d: %v", config.Width, config.Height, test.wantW, test.wantH, err)
			}
			if test.width == test.wantW && test.height == test.wantH && !bytes.Equal(data, input.Data) {
				t.Fatal("image within the limits was re-encoded")
			}
		})
	}
}

func TestPrepareRejectsOversizedDimensionsAndUnknownPreset(t *testing.T) {
	if _, err := Prepare([]Image{testPNG(t, 17000, 4)}, 0); err == nil || !strings.Contains(err.Error(), "16384 pixels") {
		t.Fatalf("source edge limit error = %v", err)
	}
	if _, err := Prepare(nil, 1500); err == nil {
		t.Fatal("accepted an unsupported resize preset")
	}
}

func testPNG(t *testing.T, width, height int) Image {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewGray(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return Image{Data: data.Bytes()}
}
