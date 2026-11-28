package main

import (
	"bytes"
	"image/png"
	"testing"
)

func TestFormatLoopbackURL(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:17070": "http://127.0.0.1:17070",
		"0.0.0.0:17070":   "http://127.0.0.1:17070",
		"[::]:17070":      "http://127.0.0.1:17070",
		":17070":          "http://127.0.0.1:17070",
	}
	for in, want := range cases {
		if got := formatLoopbackURL(in); got != want {
			t.Fatalf("formatLoopbackURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAppIconBytesValidPNG(t *testing.T) {
	raw := appIconBytes()
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 32 {
		t.Fatalf("icon dimensions = %dx%d, want 32x32", b.Dx(), b.Dy())
	}
}

func TestShouldHideOnClose(t *testing.T) {
	if !shouldHideOnClose(false, false) {
		t.Fatal("expected hide when tray enabled and not quitting")
	}
	if shouldHideOnClose(true, false) {
		t.Fatal("expected quit when no-tray enabled")
	}
	if shouldHideOnClose(false, true) {
		t.Fatal("expected quit when quitting=true")
	}
}
