package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/snowmerak/q/v2/pkg/llm/gateway"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

type commandOptions struct {
	configPath     string
	listenOverride string
}

func run() error {
	options, err := parseOptions(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}

	watcher, err := newConfigWatcher(options.configPath)
	if err != nil {
		return fmt.Errorf("watch configuration: %w", err)
	}
	defer watcher.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	config, err := loadGatewayConfig(options)
	if err != nil {
		return err
	}
	listenAddress := config.Listen
	runtime, err := newGatewayRuntime(ctx, config)
	if err != nil {
		return err
	}
	defer runtime.Close()

	server := &http.Server{
		Addr:              listenAddress,
		Handler:           runtime,
		ReadHeaderTimeout: 10 * time.Second,
	}
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		watcher.Run(ctx, func(reloadContext context.Context) {
			replacement, loadErr := loadGatewayConfig(options)
			if loadErr != nil {
				if reloadContext.Err() == nil {
					log.Printf("configuration reload skipped: %v", loadErr)
				}
				return
			}
			if replacement.Listen != listenAddress {
				log.Printf("configuration reload: listen change to %q requires a restart; continuing on %s", replacement.Listen, listenAddress)
			}
			replaced, reloadErr := runtime.Reload(reloadContext, replacement)
			if !replaced {
				if reloadContext.Err() == nil {
					log.Printf("configuration reload skipped: %v", reloadErr)
				}
				return
			}
			if reloadErr != nil {
				log.Printf("configuration reloaded with cleanup error: %v", reloadErr)
			} else {
				log.Printf("configuration reloaded from %s", options.configPath)
			}
		})
	}()
	defer func() {
		stop()
		<-watcherDone
	}()
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	fmt.Printf("llm-provider listening on %s\n", listenAddress)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func parseOptions(args []string, output io.Writer) (commandOptions, error) {
	var options commandOptions
	flags := flag.NewFlagSet("llm-provider", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&options.configPath, "f", "llm-provider.json", "path to the JSON configuration file")
	flags.StringVar(&options.configPath, "config", "llm-provider.json", "path to the JSON configuration file (alias for -f)")
	flags.StringVar(&options.listenOverride, "listen", "", "override the configured listen address")
	if err := flags.Parse(args); err != nil {
		return commandOptions{}, err
	}
	if flags.NArg() != 0 {
		return commandOptions{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	return options, nil
}

func loadGatewayConfig(options commandOptions) (gateway.Config, error) {
	config, err := gateway.LoadConfig(options.configPath)
	if err != nil {
		return gateway.Config{}, err
	}
	if options.listenOverride != "" {
		config.Listen = options.listenOverride
	}
	if config.Listen == "" {
		config.Listen = "127.0.0.1:18181"
	}
	return config, nil
}
