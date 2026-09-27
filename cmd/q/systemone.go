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

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/systemoneconfig"
	"github.com/snowmerak/q/systemoneserver"
)

type systemOneCommandOptions struct {
	host    string
	port    int
	hostSet bool
	portSet bool
}

func runSystemOneCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	store, err := config.DefaultStore()
	if err != nil {
		return fmt.Errorf("q systemone: %w", err)
	}
	return runSystemOneWithStore(ctx, args, stdout, stderr, systemoneconfig.Store{Dir: store.Dir})
}

func runSystemOneWithStore(
	ctx context.Context, args []string, stdout, stderr io.Writer, store systemoneconfig.Store,
) error {
	options, err := parseSystemOneOptions(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("q systemone: %w", err)
	}
	value, err := store.LoadOrDefault()
	if err != nil {
		return fmt.Errorf("q systemone: load settings: %w", err)
	}
	host, port := value.Server.Host, value.Server.Port
	if options.hostSet {
		host = options.host
	}
	if options.portSet {
		port = options.port
	}
	value.Server.Host, value.Server.Port = host, port
	instance, err := systemoneserver.New(value)
	if err != nil {
		return fmt.Errorf("q systemone: initialize: %w", err)
	}
	listener, fallback, err := listenSystemOne(options, value.Server)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{
		Handler:           instance.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	serverContext, cancelServer := context.WithCancel(ctx)
	defer cancelServer()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-serverContext.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if fallback {
		_, _ = fmt.Fprintf(stderr, "q systemone: configured port %d is unavailable; using %s\n", port, listener.Addr())
	}
	if _, err := fmt.Fprintf(stdout, "q systemone listening on http://%s/v1\n", listener.Addr()); err != nil {
		cancelServer()
		<-shutdownDone
		return fmt.Errorf("q systemone: report listen address: %w", err)
	}
	serveErr := server.Serve(listener)
	cancelServer()
	<-shutdownDone
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("q systemone: serve: %w", serveErr)
}

func parseSystemOneOptions(args []string, output io.Writer) (systemOneCommandOptions, error) {
	options := systemOneCommandOptions{port: -1}
	flags := flag.NewFlagSet("q systemone start", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(output, "usage: q systemone start [--host <ip>] [--port <port>]")
		flags.PrintDefaults()
	}
	flags.StringVar(&options.host, "host", "", "override the configured listen IP address")
	flags.IntVar(&options.port, "port", -1, "override the configured listen port (0 selects a random port)")
	if err := flags.Parse(args); err != nil {
		return systemOneCommandOptions{}, err
	}
	if flags.NArg() != 0 {
		return systemOneCommandOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	flags.Visit(func(current *flag.Flag) {
		switch current.Name {
		case "host":
			options.hostSet = true
		case "port":
			options.portSet = true
		}
	})
	if options.hostSet && net.ParseIP(options.host) == nil {
		return systemOneCommandOptions{}, fmt.Errorf("host %q is not an IP address", options.host)
	}
	if options.portSet && (options.port < 0 || options.port > 65535) {
		return systemOneCommandOptions{}, errors.New("port must be between 0 and 65535")
	}
	return options, nil
}

func listenSystemOne(options systemOneCommandOptions, configured systemoneconfig.ServerConfig) (net.Listener, bool, error) {
	address := net.JoinHostPort(configured.Host, fmt.Sprintf("%d", configured.Port))
	listener, err := net.Listen("tcp", address)
	if err == nil {
		return listener, false, nil
	}
	if options.portSet || configured.Port == 0 || !isAddressInUse(err) {
		return nil, false, fmt.Errorf("q systemone: listen on %s: %w", address, err)
	}
	fallbackAddress := net.JoinHostPort(configured.Host, "0")
	listener, fallbackErr := net.Listen("tcp", fallbackAddress)
	if fallbackErr != nil {
		return nil, false, fmt.Errorf("q systemone: listen on %s after %s was unavailable: %w", fallbackAddress, address, fallbackErr)
	}
	return listener, true, nil
}
