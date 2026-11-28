// Command cdp-q-driver controls Chromium through a local SDK session.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/snowmerak/q/v2/pkg/cdp/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Main(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
