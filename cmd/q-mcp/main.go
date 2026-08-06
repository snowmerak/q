package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/snowmerak/q/loom"
	qtools "github.com/snowmerak/q/tools"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == loom.ChildCommand {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if err := loom.RunChild(ctx, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	root := flag.String("root", ".", "workspace root exposed to builtin tools")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := qtools.RunStdio(ctx, *root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
