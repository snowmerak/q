package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/remoteapi"
	"github.com/snowmerak/q/remoteconfig"
)

func runRemoteCommand(ctx context.Context, stdout, stderr io.Writer) (returnErr error) {
	store, err := config.DefaultStore()
	if err != nil {
		return fmt.Errorf("q remote: %w", err)
	}
	return runRemoteWithStore(ctx, store, stdout, stderr)
}

func runRemoteWithStore(ctx context.Context, store config.Store, stdout, stderr io.Writer) (returnErr error) {
	settingsStore := remoteconfig.Store{Dir: store.Dir}
	settings, err := settingsStore.LoadOrDefault()
	if err != nil {
		return fmt.Errorf("q remote: load settings: %w", err)
	}
	var masterKey [32]byte
	if settings.ActiveKeyCount() > 0 {
		masterKey, err = settingsStore.LoadMasterKey()
		if err != nil {
			return fmt.Errorf("q remote: load API key master: %w", err)
		}
	}
	authenticator, err := remoteconfig.NewAuthenticator(masterKey, settings)
	if err != nil {
		return fmt.Errorf("q remote: initialize authentication: %w", err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(settings.Server.Host, fmt.Sprintf("%d", settings.Server.Port)))
	if err != nil {
		return fmt.Errorf("q remote: listen: %w", err)
	}
	defer listener.Close()

	host, err := app.NewRemoteHost(ctx, store)
	if err != nil {
		return fmt.Errorf("q remote: initialize agent host: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, host.Close()) }()
	maximumParallel := config.DefaultAgentMaxParallel
	if value, loadErr := store.Load(); loadErr == nil {
		maximumParallel = value.EffectiveAgents().MaxParallel
	}
	server := &http.Server{
		Handler:           remoteapi.NewHandler(host, authenticator, maximumParallel),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
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
	watchDone := watchRemoteSettings(serverContext, settingsStore, authenticator, stderr)

	if !authenticator.Enabled() {
		_, _ = fmt.Fprintln(stderr, "q remote: API key authentication is disabled")
		if !settings.ServerIsLoopback() {
			_, _ = fmt.Fprintln(stderr, "q remote: warning: non-loopback listener is accepting unauthenticated agent execution")
		}
	} else if !settings.ServerIsLoopback() {
		_, _ = fmt.Fprintln(stderr, "q remote: warning: bearer authentication over plain HTTP requires a trusted network or TLS reverse proxy")
	}
	if _, err := fmt.Fprintf(stdout, "q remote listening on http://%s/v1 · authentication %s\n", listener.Addr().String(), enabledLabel(authenticator.Enabled())); err != nil {
		cancelServer()
		<-shutdownDone
		<-watchDone
		return fmt.Errorf("q remote: report listen address: %w", err)
	}
	serveErr := server.Serve(listener)
	cancelServer()
	<-shutdownDone
	<-watchDone
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("q remote: serve: %w", serveErr)
}

func watchRemoteSettings(
	ctx context.Context,
	store remoteconfig.Store,
	authenticator *remoteconfig.Authenticator,
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
					_, _ = fmt.Fprintf(logOutput, "q remote: settings reload skipped: %v\n", err)
					continue
				}
				if value.ActiveKeyCount() == 0 {
					err = authenticator.Reload(value)
				} else {
					masterKey, loadErr := store.LoadMasterKey()
					if loadErr != nil {
						_, _ = fmt.Fprintf(logOutput, "q remote: settings reload skipped: %v\n", loadErr)
						continue
					}
					err = authenticator.ReloadWithMasterKey(masterKey, value)
				}
				if err != nil {
					_, _ = fmt.Fprintf(logOutput, "q remote: settings reload skipped: %v\n", err)
					continue
				}
				_, _ = fmt.Fprintf(logOutput, "q remote: authentication reloaded · %s\n", enabledLabel(authenticator.Enabled()))
			}
		}
	}()
	return done
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}
