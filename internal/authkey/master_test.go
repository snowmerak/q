package authkey

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureMasterKeyCreatesOnce(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "keys")
	path := filepath.Join(directory, "service.key")
	first := [32]byte{1, 2, 3}
	created, err := EnsureMasterKey(path, "testconfig", first, func() error {
		return os.MkdirAll(directory, 0o700)
	})
	if err != nil {
		t.Fatal(err)
	}
	if created != first {
		t.Fatalf("created key differs")
	}
	second := [32]byte{9, 8, 7}
	preserved, err := EnsureMasterKey(path, "testconfig", second, func() error {
		t.Fatal("prepare called for existing key")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if preserved != first {
		t.Fatal("existing key was replaced")
	}
}
