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
	"time"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/systemoneconfig"
	"github.com/snowmerak/q/systemoneserver"
)

type systemOneCommandOptions = networkCommandOptions

func runSystemOneCommand(ctx context.Context, command string, args []string, stdout, stderr io.Writer) error {
	store, err := config.DefaultStore()
	if err != nil {
		return fmt.Errorf("q %s: %w", command, err)
	}
	return runSystemOneWithStore(ctx, command, args, stdout, stderr, systemoneconfig.Store{Dir: store.Dir})
}

func runSystemOneWithStore(
	ctx context.Context, command string, args []string, stdout, stderr io.Writer, store systemoneconfig.Store,
) error {
	options, err := parseSystemOneOptions(command, args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("q %s: %w", command, err)
	}
	value, err := store.LoadOrDefault()
	if err != nil {
		return fmt.Errorf("q %s: load settings: %w", command, err)
	}
	host, port := value.Server.Host, value.Server.Port
	if options.hostSet {
		host = options.host
	}
	if options.portSet {
		port = options.port
	}
	value.Server.Host, value.Server.Port = host, port
	var masterKey [32]byte
	if value.ActiveGeneratedKeyCount() > 0 {
		masterKey, err = store.LoadMasterKey()
		if err != nil {
			return fmt.Errorf("q %s: load API key master: %w", command, err)
		}
	} else if value.ActiveKeyCount() == 0 {
		_, _ = fmt.Fprintf(stderr, "q %s: no active API keys. Authentication is disabled.\n", command)
	}
	instance, err := systemoneserver.NewWithMasterKey(value, masterKey)
	if err != nil {
		return fmt.Errorf("q %s: initialize: %w", command, err)
	}
	listener, fallback, err := listenSystemOne(command, options, value.Server)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
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
	watchDone := watchSystemOneKeyring(serverContext, command, store, instance, stderr)
	if fallback {
		_, _ = fmt.Fprintf(stderr, "q %s: configured port %d is unavailable. Listening on %s.\n", command, port, listener.Addr())
	}
	if _, err := fmt.Fprintf(stdout, "q %s listening on http://%s/v1\n", command, listener.Addr()); err != nil {
		cancelServer()
		<-shutdownDone
		<-watchDone
		return fmt.Errorf("q %s: report listen address: %w", command, err)
	}
	serveErr := server.Serve(listener)
	cancelServer()
	<-shutdownDone
	<-watchDone
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("q %s: serve: %w", command, serveErr)
}

func watchSystemOneKeyring(ctx context.Context, command string, store systemoneconfig.Store, server *systemoneserver.Server, output io.Writer) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		var lastInfo os.FileInfo
		if info, err := systemOneConfigSnapshot(store.Path()); err == nil {
			lastInfo = info
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				info, err := systemOneConfigSnapshot(store.Path())
				if err != nil || !systemOneConfigChanged(lastInfo, info) {
					continue
				}
				lastInfo = info
				value, err := store.LoadOrDefault()
				if err != nil {
					_, _ = fmt.Fprintf(output, "q %s: API key reload skipped: %v\n", command, err)
					continue
				}
				var masterKey [32]byte
				if value.ActiveGeneratedKeyCount() > 0 {
					masterKey, err = store.LoadMasterKey()
					if err != nil {
						_, _ = fmt.Fprintf(output, "q %s: API key reload skipped: %v\n", command, err)
						continue
					}
				}
				if err := server.ReloadAuthentication(value, masterKey); err != nil {
					_, _ = fmt.Fprintf(output, "q %s: API key reload skipped: %v\n", command, err)
					continue
				}
				_, _ = fmt.Fprintf(output, "q %s: API keys reloaded\n", command)
			}
		}
	}()
	return done
}

func systemOneConfigSnapshot(path string) (os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return file.Stat()
}

func systemOneConfigChanged(previous, current os.FileInfo) bool {
	if previous == nil {
		return true
	}
	return !os.SameFile(previous, current) || !previous.ModTime().Equal(current.ModTime())
}

func parseSystemOneOptions(args []string, output io.Writer) (systemOneCommandOptions, error) {
	return parseNetworkOptions("systemone", args, output)
}

func listenSystemOne(command string, options systemOneCommandOptions, configured systemoneconfig.ServerConfig) (net.Listener, bool, error) {
	address := net.JoinHostPort(configured.Host, fmt.Sprintf("%d", configured.Port))
	listener, err := net.Listen("tcp", address)
	if err == nil {
		return listener, false, nil
	}
	if options.portSet || configured.Port == 0 || !isAddressInUse(err) {
		return nil, false, fmt.Errorf("q %s: listen on %s: %w", command, address, err)
	}
	fallbackAddress := net.JoinHostPort(configured.Host, "0")
	listener, fallbackErr := net.Listen("tcp", fallbackAddress)
	if fallbackErr != nil {
		return nil, false, fmt.Errorf("q %s: listen on %s after %s was unavailable: %w", command, fallbackAddress, address, fallbackErr)
	}
	return listener, true, nil
}
