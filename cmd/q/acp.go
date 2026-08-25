package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/snowmerak/q/app"
)

func runACPCommand(ctx context.Context, args []string, input io.Reader, output, stderr io.Writer) error {
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
