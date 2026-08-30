package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/snowmerak/q/app"
)

type acpCommandOptions struct {
	root string
	app  app.ACPOptions
}

func runACPCommand(ctx context.Context, args []string, input io.Reader, output, stderr io.Writer) error {
	options, err := parseACPCommandOptions(args, stderr)
	if err != nil {
		return err
	}
	return app.RunACPDefault(ctx, options.root, input, output, stderr, options.app)
}

func parseACPCommandOptions(args []string, stderr io.Writer) (acpCommandOptions, error) {
	flags := flag.NewFlagSet("q acp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options acpCommandOptions
	var autoApprove, autoResolve, autonomous bool
	flags.StringVar(&options.root, "root", ".", "workspace root served by this ACP process")
	flags.BoolVar(&autoApprove, "auto-approve", false, "automatically approve valid /plan proposals for this process")
	flags.BoolVar(&autoResolve, "auto-resolve", false, "resolve Griller requirement questions with the engineering default policy")
	flags.BoolVar(&autonomous, "autonomous", false, "enable both --auto-approve and --auto-resolve")
	if err := flags.Parse(args); err != nil {
		return acpCommandOptions{}, err
	}
	if flags.NArg() != 0 {
		return acpCommandOptions{}, fmt.Errorf("usage: q acp [--root <path>] [--auto-approve] [--auto-resolve] [--autonomous]: unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	seen := make(map[string]bool)
	flags.Visit(func(current *flag.Flag) { seen[current.Name] = true })
	if seen["autonomous"] {
		options.app.Plan.AutoApprove = app.BooleanOverride{Set: true, Value: autonomous}
		options.app.Plan.AutoResolve = app.BooleanOverride{Set: true, Value: autonomous}
	}
	if seen["auto-approve"] {
		options.app.Plan.AutoApprove = app.BooleanOverride{Set: true, Value: autoApprove}
	}
	if seen["auto-resolve"] {
		options.app.Plan.AutoResolve = app.BooleanOverride{Set: true, Value: autoResolve}
	}
	return options, nil
}
