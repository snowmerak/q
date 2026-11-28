package main

import (
	"bytes"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
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
	if b := img.Bounds(); b.Dx() != 64 || b.Dy() != 64 {
		t.Fatalf("icon dimensions = %dx%d, want 64x64", b.Dx(), b.Dy())
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

type fakeAutostart struct {
	enabled  bool
	lastOpts application.AutostartOptions
	err      error
}

func (f *fakeAutostart) IsEnabled() (bool, error) { return f.enabled, f.err }
func (f *fakeAutostart) EnableWithOptions(opts application.AutostartOptions) error {
	f.lastOpts = opts
	if f.err != nil {
		return f.err
	}
	f.enabled = true
	return nil
}
func (f *fakeAutostart) Disable() error {
	if f.err != nil {
		return f.err
	}
	f.enabled = false
	return nil
}

func TestToggleAutostart(t *testing.T) {
	mgr := &fakeAutostart{}
	state, err := toggleAutostart(mgr, true)
	if err != nil || !state || !mgr.enabled {
		t.Fatalf("enable failed: state=%v enabled=%v err=%v", state, mgr.enabled, err)
	}
	if !reflect.DeepEqual(mgr.lastOpts.Arguments, []string{"--start-hidden"}) {
		t.Fatalf("unexpected autostart arguments: %+v", mgr.lastOpts.Arguments)
	}

	state, err = toggleAutostart(mgr, false)
	if err != nil || state || mgr.enabled {
		t.Fatalf("disable failed: state=%v enabled=%v err=%v", state, mgr.enabled, err)
	}

	failing := &fakeAutostart{enabled: false, err: errors.New("registry denied")}
	state, err = toggleAutostart(failing, true)
	if err == nil || state {
		t.Fatalf("expected rollback on error: state=%v err=%v", state, err)
	}
}

func TestPackagingIconsPresent(t *testing.T) {
	for _, rel := range []string{
		filepath.Join("packaging", "icon.ico"),
		filepath.Join("packaging", "icons.icns"),
	} {
		info, err := os.Stat(rel)
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if info.Size() == 0 {
			t.Fatalf("%s is empty", rel)
		}
	}
}
