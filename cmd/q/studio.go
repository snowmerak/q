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
	"sync"
	"time"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/diagnostics"
	"github.com/snowmerak/q/internal/studioprocess"
	"github.com/snowmerak/q/studio"
	"github.com/snowmerak/q/worklock"
)

type studioCommandOptions struct {
	host   string
	port   int
	noOpen bool
}

func runStudioCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return runStudioAt(ctx, args, stdout, stderr, openBrowserURL, "/")
}

func runStudioAt(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	open func(string) error,
	initialPath string,
) (returnErr error) {
	options, err := parseStudioOptions(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("q studio: %w", err)
	}
	return serveStudio(ctx, options, stdout, stderr, open, initialPath, nil)
}

// A desktop owner receives readiness over its inherited pipe and keeps stdin
// open. Closing that pipe cancels only a runtime created by this invocation.
type studioDesktopOwner struct {
	ready func(studioprocess.Endpoint) error
}

func serveStudio(ctx context.Context, options studioCommandOptions, stdout, stderr io.Writer, open func(string) error, initialPath string, desktop *studioDesktopOwner) (returnErr error) {
	store, err := config.DefaultStore()
	if err != nil {
		return fmt.Errorf("q studio: %w", err)
	}
	lock, err := studioprocess.Acquire(store.Dir)
	if errors.Is(err, worklock.ErrLocked) {
		endpoint, err := studioprocess.Discover(ctx, store.Dir)
		if err != nil {
			return err
		}
		if desktop == nil && (options.port != 0 && endpoint.Port != options.port || options.host != "127.0.0.1" && endpoint.Host != options.host) {
			return errors.New("q studio: another Studio owns this profile with a different address; stop it before changing --host or --port")
		}
		if desktop != nil {
			if err := desktop.ready(endpoint); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		}
		_, _ = fmt.Fprintf(stdout, "q studio already running on %s\n", endpoint.URL)
		if !options.noOpen && open != nil {
			browserURL, err := endpoint.BrowserURL(ctx, initialPath)
			if err != nil {
				return err
			}
			return open(browserURL)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("q studio: acquire runtime: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	defer studioprocess.Remove(store.Dir)
	loaded, loadErr := store.Load()
	logging := loaded.Logging.Effective()
	invalidLogging := logging.Validate() != nil
	if invalidLogging {
		logging = (config.LoggingConfig{}).Effective()
	}
	logs, err := diagnostics.Open(store.Dir, diagnostics.Options{
		Level: logging.LogLevel(), Stdout: *logging.Stdout, FileEnabled: *logging.FileEnabled,
		MaxFileBytes: logging.MaxFileMiB * (1 << 20), MaxTotalBytes: logging.MaxTotalBytes,
		MaxAge: time.Duration(logging.MaxAgeDays) * 24 * time.Hour,
	}, stdout, stderr)
	if err != nil {
		return fmt.Errorf("q studio: initialize diagnostics: %w", err)
	}
	defer func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := logs.Close(closeContext); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
		}
	}()
	logger := logs.Logger()
	ctx = diagnostics.WithLogger(ctx, logger)
	if invalidLogging || (loadErr != nil && !errors.Is(loadErr, config.ErrNotFound)) {
		logger.Warn("studio.logging.config_fallback", "reason_code", "invalid_config")
	}
	logger.Info("studio.started", "host", options.host, "port", options.port, "max_file_bytes", logging.MaxFileMiB*(1<<20), "max_total_bytes", logging.MaxTotalBytes, "max_age_days", logging.MaxAgeDays, "stdout", *logging.Stdout)
	defer func() {
		if returnErr != nil {
			logger.Error("studio.stopped", "error", returnErr)
		} else {
			logger.Info("studio.stopped")
		}
	}()
	handler, err := studio.NewServer(ctx, studio.WithLogger(logger))
	if err != nil {
		return fmt.Errorf("q studio: initialize: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, handler.Close()) }()
	listener, err := net.Listen("tcp", net.JoinHostPort(options.host, fmt.Sprintf("%d", options.port)))
	if err != nil {
		return fmt.Errorf("q studio: listen: %w", err)
	}
	defer func() { _ = listener.Close() }()
	if !net.ParseIP(options.host).IsLoopback() {
		logger.Warn("studio.listener.exposed", "host", options.host)
	}

	_, listenPort, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return fmt.Errorf("q studio: resolve listen address: %w", err)
	}
	endpoint := studioprocess.Endpoint{
		Protocol: studioprocess.Protocol, URL: "http://" + net.JoinHostPort(studioClientHost(options.host), listenPort),
		Host: options.host, Port: listener.Addr().(*net.TCPAddr).Port,
		Token: studioprocess.Secret(), Instance: studioprocess.Secret(), Secure: desktop != nil, Owned: true,
	}
	server := &http.Server{
		Handler:           studioprocess.Access(endpoint, handler),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          diagnostics.HTTPErrorLogger(logger),
	}
	serverContext, cancelServer := context.WithCancel(ctx)
	var services sync.WaitGroup
	services.Go(func() {
		runStudioGateway(serverContext, store.Dir, stdout, stderr)
	})
	services.Go(func() {
		runStudioDecisions(serverContext, store.Dir, stdout, stderr)
	})
	defer func() {
		cancelServer()
		services.Wait()
	}()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-serverContext.Done()
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		if err := server.Shutdown(shutdownContext); err != nil {
			logger.Error("studio.shutdown.failed", "error", err)
		}
	}()

	listenURL := "http://" + listener.Addr().String()
	openURL := "http://" + net.JoinHostPort(studioClientHost(options.host), listenPort)
	if initialPath != "" && initialPath != "/" {
		path := "/" + strings.TrimPrefix(initialPath, "/")
		listenURL += path
		openURL += path
	}
	if _, err := fmt.Fprintf(stdout, "q studio listening on %s\n", listenURL); err != nil {
		cancelServer()
		<-shutdownDone
		return fmt.Errorf("q studio: report listen address: %w", err)
	}
	// A cold browser launch may block; serve its first request independently.
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	if err := studioprocess.Publish(store.Dir, endpoint); err != nil {
		cancelServer()
		<-shutdownDone
		return fmt.Errorf("q studio: publish runtime: %w", err)
	}
	if desktop != nil {
		if err := desktop.ready(endpoint); err != nil {
			cancelServer()
			<-shutdownDone
			return fmt.Errorf("q studio: report desktop readiness: %w", err)
		}
	}
	logger.Info("studio.ready", "address", listener.Addr().String())
	if !options.noOpen && open != nil {
		if err := open(openURL); err != nil {
			logger.Warn("studio.browser.failed", "error", err)
		}
	}
	serveErr := <-serveDone
	cancelServer()
	<-shutdownDone
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("q studio: serve: %w", serveErr)
}

func parseStudioOptions(args []string, output io.Writer) (studioCommandOptions, error) {
	options := studioCommandOptions{host: "127.0.0.1"}
	flags := flag.NewFlagSet("q studio", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(output, "usage: q studio [--host <ip>] [--port <port>] [--no-open]")
		flags.PrintDefaults()
	}
	flags.StringVar(&options.host, "host", "127.0.0.1", "listen IP address")
	flags.IntVar(&options.port, "port", 0, "listen port (0 selects a random port)")
	flags.BoolVar(&options.noOpen, "no-open", false, "do not open the Studio URL in a browser")
	if err := flags.Parse(args); err != nil {
		return studioCommandOptions{}, err
	}
	if flags.NArg() != 0 {
		return studioCommandOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if net.ParseIP(options.host) == nil {
		return studioCommandOptions{}, fmt.Errorf("host %q is not an IP address", options.host)
	}
	if options.port < 0 || options.port > 65535 {
		return studioCommandOptions{}, fmt.Errorf("port must be between 0 and 65535")
	}
	return options, nil
}

func studioClientHost(host string) string {
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsUnspecified() {
		return host
	}
	if ip.To4() != nil {
		return "127.0.0.1"
	}
	return "::1"
}
