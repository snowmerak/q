package fsopen

import (
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
