package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/snowmerak/q/diagnostics"
	"github.com/snowmerak/q/gatewayconfig"
	"github.com/snowmerak/q/internal/configwatch"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/systemoneconfig"
)

func runStudioGateway(ctx context.Context, directory string, stdout, stderr io.Writer) {
	providers := providerhost.Store{Dir: directory}
	settings := gatewayconfig.Store{Dir: directory}
	runStudioService(ctx, studioService{
		name:     "Gateway",
		paths:    []string{providers.Path(), settings.Path()},
		settings: func() (any, error) { return gatewayServiceSettings(providers, settings) },
		run: func(ctx context.Context) error {
			return runGatewayWithStore(ctx, nil, stdout, stderr, providers, settings)
		},
	}, stderr)
}

func runStudioDecisions(ctx context.Context, directory string, stdout, stderr io.Writer) {
	settings := systemoneconfig.Store{Dir: directory}
	runStudioService(ctx, studioService{
		name:     "Decisions",
		paths:    []string{settings.Path()},
		settings: func() (any, error) { return decisionsServiceSettings(settings) },
		run: func(ctx context.Context) error {
			return runSystemOneWithStore(ctx, "decisions", nil, stdout, stderr, settings)
		},
	}, stderr)
}

// studioService describes a standalone server that Studio runs.
type studioService struct {
	name  string
	paths []string
	// settings returns the validated settings that require a restart to take
	// effect. API keys are left out because the running server reloads them.
	settings func() (any, error)
	run      func(context.Context) error
}

// gatewayServiceSettings returns the provider and listener settings that the
// Gateway reads only at startup.
func gatewayServiceSettings(providers providerhost.Store, settings gatewayconfig.Store) (any, error) {
	value, err := providers.Load()
	if err != nil && !errors.Is(err, providerhost.ErrNotFound) {
		return nil, err
	}
	server, err := settings.LoadOrDefault()
	if err != nil {
		return nil, err
	}
	return struct {
		Providers any
		Server    gatewayconfig.ServerConfig
	}{value, server.Server}, nil
}

// decisionsServiceSettings returns the Decisions settings other than its
// API keys.
func decisionsServiceSettings(settings systemoneconfig.Store) (any, error) {
	value, err := settings.LoadOrDefault()
	if err != nil {
		return nil, err
	}
	value.APIKeys = nil
	value.Server.APIKey = ""
	return value, nil
}

// sameStudioServiceSettings compares settings by their JSON encoding, which
// covers values such as slices and maps that are not comparable with ==.
func sameStudioServiceSettings(left, right any) bool {
	leftBody, leftErr := json.Marshal(left)
	rightBody, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftBody) == string(rightBody)
}

// runStudioService runs a standalone server for the lifetime of its owning
// Studio. It restarts the server when the settings it reads at startup change
// and retries a server that stopped once its configuration changes.
func runStudioService(ctx context.Context, service studioService, stderr io.Writer) {
	logger := diagnostics.Logger(ctx)
	event := "studio." + strings.ToLower(service.name)
	watcher := configwatch.New(ctx, service.paths, configwatch.Options{})
	defer func() { <-watcher.Done() }()
	for ctx.Err() == nil {
		logger.Info(event + ".starting")
		applied, _ := service.settings()
		runContext, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- service.run(runContext) }()

		var runErr error
		stopped := false
	watch:
		for {
			select {
			case <-ctx.Done():
				cancel()
				runErr = <-done
				break watch
			case runErr = <-done:
				stopped = true
				break watch
			case <-watcher.Changes():
				current, err := service.settings()
				if err != nil {
					// Keep the running server and its last working settings.
					logger.Warn(event+".reload_skipped", "reason_code", "invalid_configuration", "error", err)
					_, _ = fmt.Fprintf(stderr, "q studio: %s settings not applied: %v\n", service.name, err)
					continue
				}
				if sameStudioServiceSettings(applied, current) {
					continue
				}
				logger.Info(event+".restarting", "reason_code", "configuration_changed")
				cancel()
				runErr = <-done
				break watch
			}
		}
		cancel()
		if ctx.Err() != nil {
			logger.Info(event + ".stopped")
			return
		}
		if stopped {
			if runErr != nil {
				logger.Warn(event+".unavailable", "error", runErr)
				_, _ = fmt.Fprintf(stderr, "q studio: standalone %s unavailable: %v\n", service.name, runErr)
			}
			select {
			case <-ctx.Done():
			case <-watcher.Changes():
			}
		}
	}
}
