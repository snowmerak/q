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

func TestParseACPCommandRejectsRetiredPlanFlags(t *testing.T) {
	for _, flag := range []string{"--auto-approve", "--auto-resolve", "--autonomous"} {
		if _, err := parseACPCommandOptions([]string{flag}, io.Discard); err == nil {
			t.Fatalf("retired flag %s was accepted", flag)
		}
	}
}
