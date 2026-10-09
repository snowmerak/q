//go:build windows

package fsreplace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRetryBacksOffWithinBound(t *testing.T) {
	attempts := 0
	var delays []time.Duration
	err := retry(func() error {
		attempts++
		if attempts <= maximumReplaceRetries {
			return fmt.Errorf("replace: %w", syscall.ERROR_ACCESS_DENIED)
		}
		return nil
	}, func(delay time.Duration) {
		delays = append(delays, delay)
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond,
		80 * time.Millisecond, 160 * time.Millisecond, 320 * time.Millisecond, 640 * time.Millisecond}
	if attempts != maximumReplaceRetries+1 || len(delays) != len(want) {
		t.Fatalf("attempts = %d, delays = %v", attempts, delays)
	}
	for index := range want {
		if delays[index] != want[index] {
			t.Fatalf("delays = %v, want %v", delays, want)
		}
	}
}

func TestRetryHandlesUnableToRemoveReplaced(t *testing.T) {
	attempts := 0
	err := retry(func() error {
		attempts++
		if attempts == 1 {
			return fmt.Errorf("replace: %w", errorUnableToRemoveReplaced)
		}
		return nil
	}, func(time.Duration) {})
	if err != nil || attempts != 2 {
		t.Fatalf("error = %v, attempts = %d", err, attempts)
	}
}

func TestRetryDoesNotRepeatPartialReplacement(t *testing.T) {
	for _, code := range []syscall.Errno{1176, 1177} {
		attempts := 0
		err := retry(func() error {
			attempts++
			return fmt.Errorf("replace: %w", code)
		}, func(time.Duration) {})
		if !errors.Is(err, code) || attempts != 1 {
			t.Fatalf("code = %d, error = %v, attempts = %d", code, err, attempts)
		}
	}
}

func TestReplaceRetainsBothFilesWhenReaderBlocksRemoval(t *testing.T) {
	directory := t.TempDir()
	source, destination := filepath.Join(directory, "source"), filepath.Join(directory, "destination")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	}()
	err = Replace(source, destination)
	if err == nil || !strings.Contains(err.Error(), destination) {
		t.Fatalf("replace while reader blocks removal = %v", err)
	}
	for path, want := range map[string]string{source: "new", destination: "old"} {
		body, readErr := os.ReadFile(path)
		if readErr != nil || string(body) != want {
			t.Fatalf("file %q = %q, %v; want %q", path, body, readErr, want)
		}
	}
}

func TestRetryReturnsFinalTransientError(t *testing.T) {
	attempts := 0
	err := retry(func() error {
		attempts++
		return fmt.Errorf("replace: %w", errorSharingViolation)
	}, func(time.Duration) {})
	if !errors.Is(err, errorSharingViolation) || attempts != maximumReplaceRetries+1 {
		t.Fatalf("error = %v, attempts = %d", err, attempts)
	}
}

func TestRetryDoesNotRetryPermanentError(t *testing.T) {
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
