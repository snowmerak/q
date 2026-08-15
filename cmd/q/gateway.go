package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/gatewayconfig"
	"github.com/snowmerak/q/providerhost"
)

type gatewayCommandOptions struct {
	host    string
	port    int
	hostSet bool
	portSet bool
}

func runGatewayCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	store, err := config.DefaultStore()
	if err != nil {
		return fmt.Errorf("q gateway: %w", err)
	}
	return runGatewayWithStore(
		ctx, args, stdout, stderr,
		providerhost.Store{Dir: store.Dir}, gatewayconfig.Store{Dir: store.Dir},
	)
}

func runGatewayWithStore(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	providerStore providerhost.Store,
	settingsStore gatewayconfig.Store,
) error {
	options, err := parseGatewayOptions(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("q gateway: %w", err)
	}

	value, err := providerStore.Load()
	if err != nil {
		return fmt.Errorf("q gateway: load providers: %w", err)
	}
	settings, err := settingsStore.LoadOrDefault()
	if err != nil {
		return fmt.Errorf("q gateway: load settings: %w", err)
	}
	var masterKey [32]byte
	if settings.ActiveKeyCount() > 0 {
		masterKey, err = settingsStore.LoadMasterKey()
		if err != nil {
			return fmt.Errorf("q gateway: load API key master: %w", err)
		}
	} else {
		_, _ = fmt.Fprintln(stderr, "q gateway: no active API keys; authentication disabled")
	}
	authenticator, err := gatewayconfig.NewAuthenticator(masterKey, settings)
	if err != nil {
		return fmt.Errorf("q gateway: initialize authentication: %w", err)
	}
	listener, fallback, err := listenGateway(options, settings.Server)
	if err != nil {
		return err
	}
	defer listener.Close()
	instance, err := gateway.NewContext(ctx, value)
	if err != nil {
		return fmt.Errorf("q gateway: initialize: %w", err)
	}
	defer instance.Close()

	server := &http.Server{
		Handler: authenticator.OptionalHandler(providerhost.ModelGroupHandler(
			instance.Handler(), config.Store{Dir: providerStore.Dir},
		)),
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
	watchDone := watchGatewayKeyring(serverContext, settingsStore, authenticator, stderr)

	if fallback {
		_, _ = fmt.Fprintf(stderr, "q gateway: configured port %d is unavailable; using %s\n", settings.Server.Port, listener.Addr().String())
	}
	if _, err := fmt.Fprintf(stdout, "q gateway listening on http://%s/v1\n", listener.Addr().String()); err != nil {
		cancelServer()
		<-shutdownDone
		<-watchDone
		return fmt.Errorf("q gateway: report listen address: %w", err)
	}
	serveErr := server.Serve(listener)
	cancelServer()
	<-shutdownDone
	<-watchDone
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("q gateway: serve: %w", serveErr)
}

func parseGatewayOptions(args []string, output io.Writer) (gatewayCommandOptions, error) {
	options := gatewayCommandOptions{port: -1}
	flags := flag.NewFlagSet("q gateway", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(output, "usage:")
		_, _ = fmt.Fprintln(output, "  q gateway [--host <ip>] [--port <port>]")
		_, _ = fmt.Fprintln(output, "  q gateway config")
		flags.PrintDefaults()
	}
	flags.StringVar(&options.host, "host", "", "override the configured listen IP address")
	flags.IntVar(&options.port, "port", -1, "override the configured listen port (0 selects a random port)")
	if err := flags.Parse(args); err != nil {
		return gatewayCommandOptions{}, err
	}
	if flags.NArg() != 0 {
		return gatewayCommandOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
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
		return gatewayCommandOptions{}, fmt.Errorf("host %q is not an IP address", options.host)
	}
	if options.portSet && (options.port < 0 || options.port > 65535) {
		return gatewayCommandOptions{}, fmt.Errorf("port must be between 0 and 65535")
	}
	return options, nil
}

func listenGateway(options gatewayCommandOptions, configured gatewayconfig.ServerConfig) (net.Listener, bool, error) {
	host := configured.Host
	port := configured.Port
	if options.hostSet {
		host = options.host
	}
	if options.portSet {
		port = options.port
	}
	address := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	listener, err := net.Listen("tcp", address)
	if err == nil {
		return listener, false, nil
	}
	if options.portSet || port == 0 || !isAddressInUse(err) {
		return nil, false, fmt.Errorf("q gateway: listen on %s: %w", address, err)
	}
	fallbackAddress := net.JoinHostPort(host, "0")
	listener, fallbackErr := net.Listen("tcp", fallbackAddress)
	if fallbackErr != nil {
		return nil, false, fmt.Errorf("q gateway: listen on %s after %s was unavailable: %w", fallbackAddress, address, fallbackErr)
	}
	return listener, true, nil
}

func isAddressInUse(err error) bool {
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == syscall.Errno(10048)
}

func watchGatewayKeyring(
	ctx context.Context,
	store gatewayconfig.Store,
	authenticator *gatewayconfig.Authenticator,
	logOutput io.Writer,
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		var lastModified time.Time
		if info, err := os.Stat(store.Path()); err == nil {
			lastModified = info.ModTime()
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				info, err := os.Stat(store.Path())
				if err != nil || info.ModTime().Equal(lastModified) {
					continue
				}
				lastModified = info.ModTime()
				value, err := store.Load()
				if err != nil {
					_, _ = fmt.Fprintf(logOutput, "q gateway: API key reload skipped: %v\n", err)
					continue
				}
				var reloadErr error
				if value.ActiveKeyCount() == 0 {
					reloadErr = authenticator.Reload(value)
				} else {
					masterKey, masterErr := store.LoadMasterKey()
					if masterErr != nil {
						_, _ = fmt.Fprintf(logOutput, "q gateway: API key reload skipped: %v\n", masterErr)
						continue
					}
					reloadErr = authenticator.ReloadWithMasterKey(masterKey, value)
				}
				if reloadErr != nil {
					_, _ = fmt.Fprintf(logOutput, "q gateway: API key reload skipped: %v\n", reloadErr)
					continue
				}
				_, _ = fmt.Fprintln(logOutput, "q gateway: API keys reloaded")
			}
		}
	}()
	return done
}
