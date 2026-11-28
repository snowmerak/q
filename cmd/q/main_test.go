package main

import (
	"context"
	"testing"
)

func TestRunCDPHelpDoesNotStartServer(t *testing.T) {
	if code := run(context.Background(), []string{"cdp", "help"}); code != 0 {
		t.Fatalf("expected exit code 0 for q cdp help, got %d", code)
	}
}
