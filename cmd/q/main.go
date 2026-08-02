package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/snowmerak/q/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := app.RunDefault(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
