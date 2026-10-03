package fsopen

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenReadsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projection.json")
	if err := os.WriteFile(path, []byte("saved"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "saved" {
		t.Fatalf("body = %q", body)
	}
}

func TestReadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projection.json")
	if _, err := ReadFile(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error = %v", err)
	}
	if err := os.WriteFile(path, []byte("saved"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := ReadFile(path)
	if err != nil || string(body) != "saved" {
		t.Fatalf("read = %q, %v", body, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("read handle was not closed: %v", err)
	}
}
