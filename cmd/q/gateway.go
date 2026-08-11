package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/providerhost"
)

const (
	defaultGatewayHost = "127.0.0.1"
	defaultGatewayPort = 0
)

type gatewayCommandOptions struct {
	host string
	port int
}

func runGatewayCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	store, err := config.DefaultStore()
	if err != nil {
		return fmt.Errorf("q gateway: %w", err)
	}
	return runGatewayWithStore(ctx, args, stdout, stderr, providerhost.Store{Dir: store.Dir})
}

func runGatewayWithStore(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	store providerhost.Store,
) error {
	options, err := parseGatewayOptions(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("q gateway: %w", err)
	}

	value, err := store.Load()
	if err != nil {
		return fmt.Errorf("q gateway: load providers: %w", err)
	}
	address := net.JoinHostPort(options.host, fmt.Sprintf("%d", options.port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("q gateway: listen on %s: %w", address, err)
	}
	defer listener.Close()
	instance, err := gateway.NewContext(ctx, value)
	if err != nil {
		return fmt.Errorf("q gateway: initialize: %w", err)
	}
	defer instance.Close()
	apiKey, err := providerhost.NewEphemeralAPIKey()
	if err != nil {
		return fmt.Errorf("q gateway: %w", err)
	}

	server := &http.Server{
		Handler:           providerhost.AuthenticatedHandler(apiKey, instance.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	serverContext, cancelServer := context.WithCancel(ctx)
	defer cancelServer()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-serverContext.Done()
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		_ = server.Shutdown(shutdownContext)
	}()

	if _, err := fmt.Fprintf(
		stdout,
		"q gateway listening on http://%s/v1\nq gateway API key: %s\n",
		listener.Addr().String(), apiKey,
	); err != nil {
		cancelServer()
		<-shutdownDone
		return fmt.Errorf("q gateway: report listen address: %w", err)
	}
	serveErr := server.Serve(listener)
	cancelServer()
	<-shutdownDone
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("q gateway: serve: %w", serveErr)
}

func parseGatewayOptions(args []string, output io.Writer) (gatewayCommandOptions, error) {
	options := gatewayCommandOptions{host: defaultGatewayHost, port: defaultGatewayPort}
	flags := flag.NewFlagSet("q gateway", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(output, "usage:")
		_, _ = fmt.Fprintln(output, "  q gateway [--host <ip>] [--port <port>]")
		_, _ = fmt.Fprintln(output, "  q gateway config")
		flags.PrintDefaults()
	}
	flags.StringVar(&options.host, "host", options.host, "IP address to listen on")
	flags.IntVar(&options.port, "port", options.port, "port to listen on (default 0 selects a random port)")
	if err := flags.Parse(args); err != nil {
		return gatewayCommandOptions{}, err
	}
	if flags.NArg() != 0 {
		return gatewayCommandOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if net.ParseIP(options.host) == nil {
		return gatewayCommandOptions{}, fmt.Errorf("host %q is not an IP address", options.host)
	}
	if options.port < 0 || options.port > 65535 {
		return gatewayCommandOptions{}, fmt.Errorf("port must be between 0 and 65535")
	}
	return options, nil
}
