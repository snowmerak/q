package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCommitCommandUsesTerminalWorkflow(t *testing.T) {
	if os.Getenv("Q_TEST_COMMIT_COMMAND") == "1" {
		os.Args = []string{"q", "commit"}
		main()
		return
	}
	// A non-repository must fail in the Git-backed commit workflow before a
	// terminal or provider is opened, rather than hosting Studio Changes.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommitCommandUsesTerminalWorkflow$")
	command.Dir = t.TempDir()
	command.Env = append(os.Environ(), "Q_TEST_COMMIT_COMMAND=1", "HOME="+command.Dir, "USERPROFILE="+command.Dir)
	body, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("q commit did not enter the terminal workflow: %v\n%s", ctx.Err(), body)
	}
	if err == nil || !strings.Contains(strings.ToLower(string(body)), "not a git repository") || strings.Contains(string(body), "q studio listening") {
		t.Fatalf("q commit should reject a non-repository without opening Studio: err=%v output=%s", err, body)
	}
}
