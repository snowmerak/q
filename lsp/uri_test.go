package lsp

import (
	"runtime"
	"strings"
	"testing"
)

func TestFileURI(t *testing.T) {
	uri, err := FileURI(".")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(uri), "file://") {
		t.Fatalf("URI = %q", uri)
	}
	if runtime.GOOS == "windows" && !strings.HasPrefix(string(uri), "file:///") {
		t.Fatalf("Windows URI = %q", uri)
	}
}

func TestFileURIRejectsEmptyPath(t *testing.T) {
	if _, err := FileURI("  "); err == nil {
		t.Fatal("empty path was accepted")
	}
}
