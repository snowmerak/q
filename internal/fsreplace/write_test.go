package fsreplace

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileCreatesAndReplaces(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.json")
	for _, content := range []string{"first", "replacement"} {
		if err := WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(path)
		if err != nil || string(body) != content {
			t.Fatalf("content = %q, error = %v", body, err)
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("private file permissions: info = %v, error = %v", info, err)
			}
		}
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 1 || entries[0].Name() != "settings.json" {
			t.Fatalf("temporary file leaked: entries = %v, error = %v", entries, err)
		}
	}
}

func TestWriteFileFailedReplacementPreservesDestinationAndCleansTemporary(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "settings.json")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(destination, "original")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(destination, []byte("replacement"), 0o600); err == nil {
		t.Fatal("replacing a nonempty directory succeeded")
	}
	body, err := os.ReadFile(sentinel)
	if err != nil || string(body) != "keep" {
		t.Fatalf("destination changed: content = %q, error = %v", body, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "settings.json" {
		t.Fatalf("temporary file leaked: entries = %v, error = %v", entries, err)
	}
}
