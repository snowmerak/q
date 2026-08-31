package main

import (
	"context"
	"io"
	"strings"

	"github.com/snowmerak/q/app"
)

type reviewRunner func(context.Context, string, io.Writer) error

func runReviewCommand(ctx context.Context, args []string, output io.Writer) error {
	return runReviewCommandWith(ctx, args, output, app.RunReviewDefault)
}

func runReviewCommandWith(ctx context.Context, args []string, output io.Writer, run reviewRunner) error {
	request := strings.TrimSpace(strings.Join(args, " "))
	return run(ctx, request, output)
}
