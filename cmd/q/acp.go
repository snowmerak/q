package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/snowmerak/q/app"
)

func runACPCommand(ctx context.Context, args []string, input io.Reader, output, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "connect" {
		return runACPConnectCommand(ctx, args[1:], stderr)
	}
	flags := flag.NewFlagSet("q acp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "workspace root served by this ACP process")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("usage: q acp [--root <path>]")
	}
	return app.RunACPDefault(ctx, *root, input, output, stderr)
}

func runACPConnectCommand(ctx context.Context, args []string, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: q acp connect <codex|grok> [--root <path>] [--session <id>] [--auth <method>]")
	}
	agent := args[0]
	flags := flag.NewFlagSet("q acp connect "+agent, flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "workspace root for the ACP session")
	sessionID := flags.String("session", "", "existing ACP session ID to load or resume")
	authMethod := flags.String("auth", "", "ACP authentication method ID")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("usage: q acp connect <codex|grok> [--root <path>] [--session <id>] [--auth <method>]")
	}
	return app.RunACPClientDefault(ctx, app.ACPClientOptions{
		Agent: agent, Root: *root, SessionID: *sessionID, AuthMethod: *authMethod,
	}, stderr)
}
