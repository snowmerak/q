package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicCreatesAndReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	if err := WriteFileAtomic(path, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatalf("first WriteFileAtomic: %v", err)
	}
	if err := WriteFileAtomic(path, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatalf("second WriteFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != `{"version":2}` {
		t.Fatalf("got %q, want v2", string(got))
	}
}

func TestInstanceLockExclusivity(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "instance.lock")
	first, err := AcquireInstanceLock(lockPath)
	if err != nil {
		t.Fatalf("first AcquireInstanceLock: %v", err)
	}
	if _, err := AcquireInstanceLock(lockPath); !errors.Is(err, ErrLocked) {
		_ = first.Release()
		t.Fatalf("expected ErrLocked on second acquire, got %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// Release is idempotent.
	if err := first.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	second, err := AcquireInstanceLock(lockPath)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	_ = second.Release()
}
