package main

import (
	"context"
	"flag"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/internal/librarymcp"
	"github.com/snowmerak/q/library"
)

func runLibraryMCP(ctx context.Context, args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("q library mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("config-dir", "", "Q configuration directory containing library.json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return flag.ErrHelp
	}
	if *dir == "" {
		store, err := config.DefaultStore()
		if err != nil {
			return err
		}
		*dir = store.Dir
	}
	value, err := (library.ConfigStore{Dir: *dir}).LoadOrDefault()
	if err != nil {
		return err
	}
	// The application host owns Library. This bridge stays available when its
	// HTTP service is temporarily down; individual tool calls report errors.
	return librarymcp.NewServer(library.NewClient(value.Endpoint(), "", 0)).Run(ctx, &mcp.StdioTransport{})
}
