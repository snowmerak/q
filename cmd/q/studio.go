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

	"github.com/snowmerak/q/studio"
)

type studioCommandOptions struct {
	port   int
	noOpen bool
}

func runStudioCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return runStudio(ctx, args, stdout, stderr, openBrowserURL)
}

func runStudio(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	open func(string) error,
) (returnErr error) {
	options, err := parseStudioOptions(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("q studio: %w", err)
	}
	handler, err := studio.NewServer(ctx)
	if err != nil {
		return fmt.Errorf("q studio: initialize: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, handler.Close()) }()
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", options.port)))
	if err != nil {
		return fmt.Errorf("q studio: listen: %w", err)
	}
	defer listener.Close()

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
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

	url := "http://" + listener.Addr().String()
	if _, err := fmt.Fprintf(stdout, "q studio listening on %s\n", url); err != nil {
		cancelServer()
		<-shutdownDone
		return fmt.Errorf("q studio: report listen address: %w", err)
	}
	if !options.noOpen && open != nil {
		if err := open(url); err != nil {
			_, _ = fmt.Fprintf(stderr, "q studio: could not open browser: %v\n", err)
		}
	}
	serveErr := server.Serve(listener)
	cancelServer()
	<-shutdownDone
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("q studio: serve: %w", serveErr)
}

func parseStudioOptions(args []string, output io.Writer) (studioCommandOptions, error) {
	options := studioCommandOptions{}
	flags := flag.NewFlagSet("q studio", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(output, "usage: q studio [--port <port>] [--no-open]")
		flags.PrintDefaults()
	}
	flags.IntVar(&options.port, "port", 0, "listen port (0 selects a random port)")
	flags.BoolVar(&options.noOpen, "no-open", false, "do not open the Studio URL in a browser")
	if err := flags.Parse(args); err != nil {
		return studioCommandOptions{}, err
	}
	if flags.NArg() != 0 {
		return studioCommandOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if options.port < 0 || options.port > 65535 {
		return studioCommandOptions{}, fmt.Errorf("port must be between 0 and 65535")
	}
	return options, nil
}
