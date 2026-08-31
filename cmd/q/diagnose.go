package main

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/snowmerak/q/app"
)

type diagnoseRunner func(context.Context, string, io.Writer) error

func runDiagnoseCommand(ctx context.Context, args []string, output io.Writer) error {
	return runDiagnoseCommandWith(ctx, args, output, app.RunDiagnoseDefault)
}

func runDiagnoseCommandWith(ctx context.Context, args []string, output io.Writer, run diagnoseRunner) error {
	issue := strings.TrimSpace(strings.Join(args, " "))
	if issue == "" {
		return errors.New("usage: q diagnose <issue...>")
	}
	return run(ctx, issue, output)
}
