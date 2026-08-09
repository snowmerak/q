package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/snowmerak/q/config"
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
	options := loom.StoreOptions{}
	configStore, err := config.DefaultStore()
	if err == nil {
		if value, loadErr := configStore.Load(); loadErr == nil {
			options = value.LoomStoreOptions(nil)
		} else if !errors.Is(loadErr, config.ErrNotFound) {
			err = loadErr
		}
	}
	if err == nil {
		err = qtools.RunStdioWithLoomOptions(ctx, *root, options)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
