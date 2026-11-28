// Command q runs the single-instance q v2 server or dispatches `q cdp`
// directly to the embedded CDP browser CLI.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/snowmerak/q/v2/pkg/app"
	cdpcli "github.com/snowmerak/q/v2/pkg/cdp/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string) int {
	if len(args) > 0 && args[0] == "cdp" {
		return cdpcli.MainWithOptions(ctx, args[1:], os.Stdin, os.Stdout, os.Stderr, cdpcli.Options{
			CommandPrefix: []string{"cdp"},
		})
	}

	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("q", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dataDir := fs.String("data-dir", "", "global q data directory override (default ~/.q or $Q_DATA_DIR)")
	listen := fs.String("listen", "", "HTTP listen address override")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	instance, err := app.Open(ctx, app.Options{
		DataDir:        *dataDir,
		ListenOverride: *listen,
	})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "q: startup failed: %v\n", err)
		return 1
	}
	defer func() { _ = instance.Close(context.Background()) }()

	_, _ = fmt.Fprintf(os.Stderr, "q listening on http://%s (data-dir: %s)\n", instance.Addr(), instance.DataDir())
	if err := instance.Start(ctx); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "q: server error: %v\n", err)
		return 1
	}
	return 0
}
