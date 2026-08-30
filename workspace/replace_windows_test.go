//go:build windows

package workspace

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
	"time"
)

func TestRetryReplaceFileBacksOffThreeTimes(t *testing.T) {
	attempts := 0
	var delays []time.Duration
	err := retryReplaceFile(func() error {
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
	if attempts != maximumReplaceRetries+1 {
		t.Fatalf("attempts = %d, want %d", attempts, maximumReplaceRetries+1)
	}
	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	if len(delays) != len(want) {
		t.Fatalf("delays = %v, want %v", delays, want)
	}
	for index := range want {
		if delays[index] != want[index] {
			t.Fatalf("delays = %v, want %v", delays, want)
		}
	}
}

func TestRetryReplaceFileReturnsFinalTransientError(t *testing.T) {
	attempts := 0
	err := retryReplaceFile(func() error {
		attempts++
		return fmt.Errorf("replace: %w", errorSharingViolation)
	}, func(time.Duration) {})
	if !errors.Is(err, errorSharingViolation) {
		t.Fatalf("error = %v, want sharing violation", err)
	}
	if attempts != maximumReplaceRetries+1 {
		t.Fatalf("attempts = %d, want %d", attempts, maximumReplaceRetries+1)
	}
}

func TestRetryReplaceFileDoesNotRetryPermanentError(t *testing.T) {
	attempts := 0
	want := errors.New("permanent")
	err := retryReplaceFile(func() error {
		attempts++
		return want
	}, func(time.Duration) {})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}
