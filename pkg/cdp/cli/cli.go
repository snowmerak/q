// Package cli exposes the complete cdp-q-driver command interface for embedding
// in another Go executable, including background sessions and JavaScript runs.
package cli

import (
	"context"
	"io"

	internalcli "github.com/snowmerak/q/v2/pkg/cdp/internal/cli"
)

// Options describes how the host executable dispatches to this CLI.
type Options struct {
	// CommandPrefix contains the arguments before CDP-specific flags/commands,
	// for example []string{"cdp"} for `q cdp ...`. Background sessions re-exec
	// the same host binary with this prefix followed by the serve arguments.
	// The host must dispatch that path without starting its UI or reading stdin.
	CommandPrefix []string
}

// Main runs the standalone command interface and returns its exit code (0, 1,
// or 2). Args exclude the executable name. It does not call os.Exit or install
// signal handlers; the caller owns its context and streams.
func Main(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return MainWithOptions(ctx, args, stdin, stdout, stderr, Options{})
}

// MainWithOptions runs the same interface inside an embedding host. Args must
// exclude both the executable and CommandPrefix. Session formats, default app
// data paths, browser discovery, JSON output, and commands are unchanged.
func MainWithOptions(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, options Options) int {
	return internalcli.MainWithPrefix(ctx, args, stdin, stdout, stderr, options.CommandPrefix)
}
