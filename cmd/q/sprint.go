package main

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/snowmerak/q/app"
)

type sprintRunner func(context.Context, string, io.Writer) error

func runSprintCommand(ctx context.Context, args []string, output io.Writer) error {
	return runSprintCommandWith(ctx, args, output, app.RunSprintDefault)
}

func runSprintCommandWith(ctx context.Context, args []string, output io.Writer, run sprintRunner) error {
	objective := strings.TrimSpace(strings.Join(args, " "))
	if objective == "" {
		return errors.New("usage: q sprint <requirement...>")
	}
	return run(ctx, objective, output)
}
