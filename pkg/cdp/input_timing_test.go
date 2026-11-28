package qdriver

import (
	"context"
	"testing"
	"time"
)

func TestInputTimingBoundsAndPhysicalCharacters(t *testing.T) {
	for _, timing := range []InputTiming{{MouseSteps: -1}, {MouseSteps: 121}, {KeyHold: -time.Millisecond}, {Jitter: 3 * time.Second}} {
		if _, err := inputTiming(&timing); err == nil {
			t.Fatalf("invalid timing accepted: %+v", timing)
		}
	}
	if fast, err := inputTiming(&InputTiming{}); err != nil || fast != (InputTiming{}) {
		t.Fatalf("zero timing did not select immediate input: %+v %v", fast, err)
	}
	for r := rune(32); r <= 126; r++ {
		key, physical := characterKey(r)
		if !physical || key.text != string(r) || key.code == "" || key.virtual == 0 {
			t.Fatalf("missing physical ASCII key %q: %+v", r, key)
		}
	}
	for _, r := range []rune{'안', '🧪', '\n', '\t'} {
		if _, physical := characterKey(r); physical {
			t.Fatalf("nonphysical character mapped to a US key: %q", r)
		}
	}
	for range 200 {
		if delay := jitterDelay(35*time.Millisecond, 15*time.Millisecond); delay < 20*time.Millisecond || delay > 50*time.Millisecond {
			t.Fatalf("jitter outside documented bounds: %s", delay)
		}
		if jitterDelay(0, time.Millisecond) < 0 {
			t.Fatal("negative delay")
		}
	}
	x, y := pointerPoint(10, 20, 110, 220, 12, 12)
	if x != 110 || y != 220 {
		t.Fatalf("interpolation missed exact target: %f %f", x, y)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitInput(ctx, time.Second); err != context.Canceled {
		t.Fatalf("input delay ignored cancellation: %v", err)
	}
}
