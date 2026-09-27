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
}

func runACPCommand(ctx context.Context, args []string, input io.Reader, output, stderr io.Writer) error {
	options, err := parseACPCommandOptions(args, stderr)
	if err != nil {
		return err
	}
	return app.RunACPDefault(ctx, options.root, input, output, stderr)
}

func parseACPCommandOptions(args []string, stderr io.Writer) (acpCommandOptions, error) {
	flags := flag.NewFlagSet("q acp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options acpCommandOptions
	flags.StringVar(&options.root, "root", ".", "workspace root served by this ACP process")
	if err := flags.Parse(args); err != nil {
		return acpCommandOptions{}, err
	}
	if flags.NArg() != 0 {
		return acpCommandOptions{}, fmt.Errorf("usage: q acp [--root <path>]: unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	return options, nil
}
