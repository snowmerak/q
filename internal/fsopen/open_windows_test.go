//go:build windows

package fsopen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/snowmerak/q/internal/fsreplace"
	"golang.org/x/sys/windows"
)

func TestOpenAllowsAtomicReplacement(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "session.json")
	source := filepath.Join(directory, "replacement.json")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := fsreplace.Replace(source, destination); err != nil {
		t.Fatalf("replace while reader is open: %v", err)
	}
	body := make([]byte, 3)
	if _, err := file.Read(body); err != nil {
		t.Fatal(err)
	}
	if string(body) != "old" {
		t.Fatalf("open handle body = %q", body)
	}
	replaced, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(replaced) != "new" {
		t.Fatalf("replacement body = %q", replaced)
	}
}

func TestRetryBacksOffTransientOpenErrors(t *testing.T) {
	attempts := 0
	var delays []time.Duration
	err := retry(func() error {
		attempts++
		if attempts <= maximumOpenRetries {
			return fmt.Errorf("open: %w", windows.ERROR_SHARING_VIOLATION)
		}
		return nil
	}, func(delay time.Duration) {
		delays = append(delays, delay)
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	if attempts != maximumOpenRetries+1 || len(delays) != len(want) {
		t.Fatalf("attempts = %d, delays = %v", attempts, delays)
	}
	for index := range want {
		if delays[index] != want[index] {
			t.Fatalf("delays = %v, want %v", delays, want)
		}
	}
}

func TestRetryReturnsPermanentOpenError(t *testing.T) {
	attempts := 0
	want := errors.New("permanent")
	err := retry(func() error {
		attempts++
		return want
	}, func(time.Duration) {})
	if !errors.Is(err, want) || attempts != 1 {
		t.Fatalf("error = %v, attempts = %d", err, attempts)
	}
}

func TestRetryHandlesTransientMissingDestination(t *testing.T) {
	attempts := 0
	err := retry(func() error {
		attempts++
		if attempts == 1 {
			return os.ErrNotExist
		}
		return nil
	}, func(time.Duration) {})
	if err != nil || attempts != 2 {
		t.Fatalf("error = %v, attempts = %d", err, attempts)
	}
}
