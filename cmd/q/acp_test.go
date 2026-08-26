package main

import (
	"io"
	"strings"
	"testing"
)

func TestACPCommandDoesNotExposeConnectClient(t *testing.T) {
	err := runACPCommand(t.Context(), []string{"connect", "codex"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "usage: q acp") {
		t.Fatalf("error = %v", err)
	}
}
