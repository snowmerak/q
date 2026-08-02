package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	qtools "github.com/snowmerak/q/tools"
)

func main() {
	root := flag.String("root", ".", "workspace root exposed to builtin tools")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := qtools.RunStdio(ctx, *root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
