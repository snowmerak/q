package app

import (
	"slices"
	"testing"
)

func TestRuntimeLogBufferRetainsBoundedCompleteLines(t *testing.T) {
	buffer := newRuntimeLogBuffer(2)
	if _, err := buffer.Write([]byte("one\ntwo\nthree\n")); err != nil {
		t.Fatal(err)
	}
	if got := buffer.snapshot(); !slices.Equal(got, []string{"two", "three"}) {
		t.Fatalf("runtime logs = %#v", got)
	}
	snapshot := buffer.snapshot()
	snapshot[0] = "changed"
	if got := buffer.snapshot(); got[0] != "two" {
		t.Fatalf("snapshot mutated buffer: %#v", got)
	}
}
