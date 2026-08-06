package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/providerhost"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if len(os.Args) > 1 && os.Args[1] == loom.ChildCommand {
		if err := loom.RunChild(ctx, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == providerhost.ChildCommand {
		if err := runGatewayChild(ctx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := app.RunDefault(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runGatewayChild(parent context.Context, args []string) error {
	if len(args) != 2 || args[0] != "--config" || args[1] == "" {
		return fmt.Errorf("usage: q %s --config <path>", providerhost.ChildCommand)
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		cancel()
	}()
	return providerhost.RunChild(ctx, args[1], providerhost.EncodeReady(json.NewEncoder(os.Stdout)))
}
