//go:build windows

package fsreplace

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
	"time"
)

func TestRetryBacksOffThreeTimes(t *testing.T) {
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
	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	if attempts != maximumReplaceRetries+1 || len(delays) != len(want) {
		t.Fatalf("attempts = %d, delays = %v", attempts, delays)
	}
	for index := range want {
		if delays[index] != want[index] {
			t.Fatalf("delays = %v, want %v", delays, want)
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
